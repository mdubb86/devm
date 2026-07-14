package reconcile

import (
	"bytes"
	"context"
	"fmt"
	"os"

	"github.com/mdubb86/devm/internal/devmbundle"
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
// (there's no separate path-only artifact to pipe). For each changed
// template, this function logs a "consuming services may need restart"
// line to stderr.
//
// Returns the first error encountered; later changes are not attempted
// after a failure so the snapshot stays coherent on retry.
func ApplyLive(tr *tart.Tart, vmName string, changes []Change, cfg schema.Config, repoRoot string) error {
	var templateChanges []Change
	var envOrPathChanged bool
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
			envOrPathChanged = true
		}
	}

	if envOrPathChanged || len(templateChanges) > 0 {
		// Rebuild the bundle and pipe it into the guest at /opt/devm/ —
		// same mechanism the provisioner uses at cold-start. Nothing is
		// written to the host workspace; with-devm-env sources the new
		// .env on every subsequent exec, and (for template changes) the
		// dispatcher below reads the freshly-piped installers. Running
		// shells keep their old env until they re-exec — hence BucketLive.
		tar, err := devmbundle.Build(devmbundle.BuildInput{
			Cfg:      cfg,
			RepoRoot: repoRoot,
		})
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
	return nil
}
