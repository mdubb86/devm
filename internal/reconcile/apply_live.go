package reconcile

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/mdubb86/devm/internal/devmbundle"
	"github.com/mdubb86/devm/internal/docker"
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
// render.RenderEtcEnvironment folds cfg.Path into /etc/environment's PATH= line
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
	var maskChanges []Change
	var bundleRebuildNeeded bool
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
			// Ingress for direct services is pushed to softnet's
			// declarative expose map by the daemon, not applied in-guest.
		case KindMaskChange:
			maskChanges = append(maskChanges, c)
		}
	}

	if bundleRebuildNeeded || len(templateChanges) > 0 {
		// Rebuild the bundle and pipe it into the guest at /opt/devm/ —
		// same mechanism the provisioner uses at cold-start. Nothing is
		// written to the host workspace; with-devm-env sources the new
		// /etc/environment on every subsequent exec, and (for template changes) the
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
		// in above. Wrapper sources /etc/environment (sets $WORKSPACE etc.)
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

	if len(maskChanges) > 0 {
		workspaceVMPath := repoRoot // mirrored path; repoRoot equals both host and guest path
		if err := applyMaskChanges(tr, vmName, cfg.Project.Name, workspaceVMPath, maskChanges); err != nil {
			return err
		}
	}

	return nil
}

// buildMaskAddScript is the guest-side shell that establishes one
// mask: mkdir the guest-ext4 backing dir with correct ownership,
// mkdir the workspace target (which may not exist yet if this is a
// fresh live-add), then bind-mount. Idempotent: mountpoint check on
// the target short-circuits if the bind is already active.
func buildMaskAddScript(projectName, maskPath, workspaceVMPath string) string {
	hostPath := "/var/devm/masks/" + projectName + "/" + maskPath
	targetPath := workspaceVMPath + "/" + maskPath
	return fmt.Sprintf(`set -e
sudo mkdir -p %s
sudo chown devm:devm %s
sudo mkdir -p %s
if mountpoint -q %s; then
    exit 0
fi
sudo mount --bind %s %s
`, hostPath, hostPath, targetPath, targetPath, hostPath, targetPath)
}

// buildMaskRemoveScript unmounts a mask. Idempotent: mountpoint
// check short-circuits if the mount is already gone (e.g. someone
// unmounted it by hand). The backing dir under /var/devm/masks/ is
// NOT deleted -- mask contents are preserved for a possible future
// re-attach.
func buildMaskRemoveScript(maskPath, workspaceVMPath string) string {
	targetPath := workspaceVMPath + "/" + maskPath
	return fmt.Sprintf(`set -e
if ! mountpoint -q %s; then
    exit 0
fi
sudo umount %s
`, targetPath, targetPath)
}

// applyMaskChanges runs one guest exec per mask add/remove. EBUSY
// on umount surfaces the spec's exact error message. Runs
// sequentially -- mask ops are cheap.
func applyMaskChanges(tr *tart.Tart, vmName, projectName, workspaceVMPath string, changes []Change) error {
	for _, c := range changes {
		if c.Kind != KindMaskChange {
			continue
		}
		var script string
		var opDesc string
		switch {
		case c.Old == "" && c.New != "":
			script = buildMaskAddScript(projectName, c.New, workspaceVMPath)
			opDesc = "add"
		case c.Old != "" && c.New == "":
			script = buildMaskRemoveScript(c.Old, workspaceVMPath)
			opDesc = "remove"
		default:
			continue
		}
		r := tr.ExecStdin(context.Background(), vmName,
			strings.NewReader(script),
			[]string{"bash", "-e", "-o", "pipefail"})
		if r.ExitCode != 0 {
			// EBUSY on umount -> structured error per spec.
			stderrLower := strings.ToLower(r.Stderr)
			if opDesc == "remove" &&
				(strings.Contains(stderrLower, "target is busy") ||
					strings.Contains(stderrLower, "device is busy")) {
				return fmt.Errorf(
					"cannot unmount mask `%s`: %s/%s is in use.\n"+
						"Stop whatever process is holding a file open under it (devm shell → check\n"+
						"your running services) and re-run devm reconcile",
					c.Old, workspaceVMPath, c.Old,
				)
			}
			return fmt.Errorf("apply_live: mask %s %q: exit %d (stderr: %s)",
				opDesc, c.Key, r.ExitCode, r.Stderr)
		}
	}
	return nil
}
