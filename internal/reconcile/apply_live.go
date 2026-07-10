package reconcile

import (
	"context"
	"fmt"
	"os"

	"github.com/mdubb86/devm/internal/devmbundle"
	"github.com/mdubb86/devm/internal/render"
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
// idempotent atomic rewrite). The installer scripts are written to
// .devm/templates/ immediately before the dispatcher runs, so the
// sandbox always executes the latest rendered content. For each
// changed template, this function logs a "consuming services may need
// restart" line to stderr.
//
// Returns the first error encountered; later changes are not attempted
// after a failure so the snapshot stays coherent on retry.
func ApplyLive(tr *tart.Tart, vmName string, changes []Change, cfg schema.Config, repoRoot string) error {
	var templateChanges []Change
	var envChanged bool
	for _, c := range changes {
		if c.Bucket() != BucketLive {
			continue
		}
		switch c.Kind {
		case KindPortAdd, KindPortRemove, KindPortChange:
			// Port changes in Tart world trigger Caddyfile reload via the
			// provisioner pattern; no host-side port publishing needed.
		case KindNetworkAdd, KindNetworkRemove:
			// Network egress is Ship 5 (iron-proxy); no apply path in Ship 4.
		case KindTemplateChange:
			templateChanges = append(templateChanges, c)
		case KindEnvAdd, KindEnvRemove, KindEnvChange:
			envChanged = true
		}
	}

	if envChanged {
		// Rewrite .devm/.env on the host. The workspace mount surfaces
		// the change inside the VM instantly; with-devm-env sources the
		// new file on every subsequent exec. Running shells keep their old
		// env until they re-exec — hence BucketLive.
		if err := render.WriteDevmEnv(cfg, repoRoot); err != nil {
			return fmt.Errorf("apply_live: write .devm/.env: %w", err)
		}
	}

	if len(templateChanges) > 0 {
		// Write updated installer scripts before running the dispatcher so
		// the sandbox executes the latest rendered content. This must happen
		// here (not earlier in RunReconcile) so the on-disk installers
		// remain as the diff baseline until the change has been detected
		// and we're committed to applying it.
		if err := render.WriteTemplateInstallers(cfg, repoRoot); err != nil {
			return fmt.Errorf("apply_live: write template installers: %w", err)
		}
		// Single dispatcher invocation re-runs all installers. The
		// dispatcher path is mounted into the VM via the workspace virtio-fs
		// share, so no transfer step is needed here. Wrapper sources
		// .devm/.env so $WORKSPACE is set — the dispatcher reads it to
		// locate .devm/templates and errors under `set -u` without it.
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
