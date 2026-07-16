package reconcile

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/mdubb86/devm/internal/devmbundle"
	"github.com/mdubb86/devm/internal/docker"
	"github.com/mdubb86/devm/internal/nftscript"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
)

// ApplyLive runs every BucketLive change through the corresponding
// operation. Non-LIVE changes in the slice are skipped silently (caller
// is expected to handle them via the recreate path).
//
// Template changes are coalesced — any number of KindTemplateChange
// entries trigger a SINGLE invocation of the in-sandbox dispatcher,
// which re-runs every installer (cheap; identical content is an
// idempotent atomic rewrite). Any env, path, or template change
// re-builds the devmbundle from cfg + repoRoot and pipes it into the
// guest at /opt/devm/ before the dispatcher runs, so the sandbox always
// executes the latest rendered content — nothing is written to the host
// workspace. Path changes ride the same rebuild as env changes because
// render.RenderEnv folds cfg.Path into the same .env's PATH= line
// (there's no separate path-only artifact to pipe). KindStartupChange is
// NOT live-applied — it's BucketRestartVM, not BucketLive, so the caller
// routes it through the recreate path (VM stop + cold start; see
// internal/provision's setupBootEnforcement / runStartupCommands, which
// pick up the freshly-rendered /opt/devm/startup.sh on that next boot).
// For each changed template, this function logs a "consuming services
// may need restart" line to stderr.
//
// Returns the first error encountered; later changes are not attempted
// after a failure so the snapshot stays coherent on retry.
func ApplyLive(tr *tart.Tart, vmName string, changes []Change, cfg schema.Config, repoRoot string, caPEM, sshAuthPub, sshHostPriv, sshHostPub []byte) error {
	var templateChanges []Change
	var bundleRebuildNeeded bool
	var directChanged bool
	for _, c := range changes {
		if c.Bucket() != BucketLive {
			continue
		}
		switch c.Kind {
		case KindPortAdd, KindPortRemove, KindPortChange:
			// Port changes in Tart world trigger Caddyfile reload via the
			// provisioner pattern; no host-side port publishing needed.
		case KindTemplateChange:
			templateChanges = append(templateChanges, c)
		case KindEnvAdd, KindEnvRemove, KindEnvChange, KindPathChange:
			bundleRebuildNeeded = true
		case KindServiceDirectChange:
			directChanged = true
		}
	}

	if bundleRebuildNeeded || len(templateChanges) > 0 {
		// Rebuild the bundle and pipe it into the guest at /opt/devm/ —
		// same mechanism the provisioner uses at cold-start. Nothing is
		// written to the host workspace; with-devm-env sources the new
		// .env on every subsequent exec, and (for template changes) the
		// dispatcher below reads the freshly-piped installers. Running
		// shells keep their old env until they re-exec — hence BucketLive.
		in := devmbundle.BuildInput{
			Cfg:                 cfg,
			RepoRoot:            repoRoot,
			CARootPEM:           caPEM,
			SSHAuthorizedPubkey: sshAuthPub,
			SSHHostPriv:         sshHostPriv,
			SSHHostPub:          sshHostPub,
		}
		if cfg.Docker {
			in.DockerRuncShim = docker.Shim()
			in.DockerCLIShim = docker.DockerShim()
		}
		tar, err := devmbundle.Build(in)
		if err != nil {
			return fmt.Errorf("apply_live: build bundle: %w", err)
		}
		r := tr.ExecStdin(context.Background(), vmName,
			bytes.NewReader(tar),
			[]string{"bash", "-e", "-o", "pipefail", "-c", devmbundle.GuestInstallScript},
		)
		if r.ExitCode != 0 {
			return fmt.Errorf("apply_live: pipe bundle: exit %d (stderr: %s)", r.ExitCode, r.Stderr)
		}
	}

	if len(templateChanges) > 0 {
		// Single dispatcher invocation re-runs all installers already piped
		// in above. Wrapper sources /opt/devm/.env (sets $WORKSPACE etc.)
		// and cd's into the workspace before exec'ing the dispatcher, which
		// itself reads the fixed /opt/devm/templates path.
		wrapperPath := devmbundle.GuestWrapper
		dispatcherPath := devmbundle.GuestDispatcher
		r := tr.ExecWithRetry(context.Background(), vmName, []string{wrapperPath, "bash", dispatcherPath})
		if r.ExitCode != 0 {
			return fmt.Errorf("apply_live: install-templates: exit %d (stderr: %s)", r.ExitCode, r.Stderr)
		}
		// User-facing "you might need to restart your service" hint.
		for _, c := range templateChanges {
			// Structural invariants (same as the rest of the Change contract):
			//   add    -> Old == "" && New != ""
			//   change -> Old != "" && New != ""
			//   remove -> Old != "" && New == ""
			if c.New == "" {
				// removed: the on-disk artifact in the sandbox persists.
				fmt.Fprintf(os.Stderr,
					"template %s removed from config; sandbox file persists until recreate.\n",
					c.Detail)
				continue
			}
			action := "updated"
			if c.Old == "" {
				action = "installed"
			}
			fmt.Fprintf(os.Stderr,
				"template %s (service %s) %s; restart consuming services in the shell if needed.\n",
				c.Detail, c.Service, action)
		}
	}

	if directChanged {
		// Reuse the SAME helpers the provisioner uses at cold-start
		// (internal/nftscript) — single source of truth for the
		// svc_ingress chain contents. nftscript.DirectPorts returns nil
		// for non-docker projects, so this is a no-op-shaped flush-to-
		// empty that correctly closes any stale direct ingress; no
		// explicit docker gate is needed here. Rebuilds from the
		// CURRENT cfg, so a removed direct service drops out
		// automatically.
		//
		// Piped straight to a single privileged errexit shell (precedent:
		// internal/serviceapi/vm.go's `tart exec -i <vm> sudo bash -s`) —
		// NOT `bash -c "cat | sudo bash"`. That double-shell form put
		// -e/-o pipefail on the OUTER bash while the script itself ran in
		// an INNER, errexit-less `sudo bash`; a failing mid-script command
		// (e.g. `nft -f -` rejecting a rule) was silently swallowed and
		// the script's exit code was just the last line's (a `list chain`
		// snapshot that succeeds regardless), so ApplyLive returned nil on
		// a partially-applied chain. Running `sudo bash -e -o pipefail -s`
		// directly makes the shell that actually interprets the script
		// errexit, so a failing command aborts it and the nonzero exit
		// propagates to the check below. The script's own `sudo` prefixes
		// become redundant-but-harmless (root re-invoking passwordless
		// sudo).
		script := nftscript.BuildSvcIngressScript(nftscript.DirectPorts(cfg))
		r := tr.ExecStdin(context.Background(), vmName,
			strings.NewReader(script),
			[]string{"sudo", "bash", "-e", "-o", "pipefail", "-s"},
		)
		if r.ExitCode != 0 {
			return fmt.Errorf("apply_live: svc_ingress: exit %d (stderr: %s)", r.ExitCode, r.Stderr)
		}
	}
	return nil
}
