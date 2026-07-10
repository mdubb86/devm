package orchestrator

import (
	"context"
	"fmt"

	"github.com/mdubb86/devm/internal/reconcile"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/serviceapi"
)

// ReconcileOptions controls RunReconcile's behaviour. Placeholder for
// future CLI-facing switches — approval / non-interactive / JSON
// decisions for a pending teardown now live in cmd/devm/reconcile.go:
// RunReconcile itself never prompts and never executes a recreate.
type ReconcileOptions struct{}

// RunReconcile POSTs cfg to the daemon's /vm/reconcile endpoint, which
// diffs it against the project's last-applied snapshot, applies every
// live-bucket change in place, and reports back what still needs a VM
// recreate (teardown_required). All diff/apply logic — and the
// .devm/lock serialization that used to guard it CLI-side — now lives
// daemon-side (internal/serviceapi/reconcile.go), keyed per-project via
// ProjectLocks.
//
// RunReconcile does not prompt or execute a recreate itself: it
// returns the daemon's classification and lets cmd/devm/reconcile.go
// decide whether to prompt, and to run the teardown + start helpers
// `devm teardown` / `devm shell` already use, on approval.
//
// Return codes: 0 on success (regardless of whether a recreate is
// pending — the caller inspects res.RecreateRequired), -1 when the
// daemon call itself failed.
func RunReconcile(cfg schema.Config, tr *tart.Tart, repoRoot string, opts ReconcileOptions) (int, ReconcileResult, error) {
	client := serviceapi.NewClient()
	resp, err := client.Reconcile(context.Background(), serviceapi.VMReconcileRequest{
		ProjectID:         cfg.Project.ID,
		VMName:            cfg.Project.VMName,
		Cfg:               cfg,
		WorkspaceHostPath: repoRoot,
	})
	if err != nil {
		return -1, ReconcileResult{}, fmt.Errorf("reconcile: %w", err)
	}

	res := ReconcileResult{
		Rendered:         true,
		SandboxState:     "running",
		Applied:          resp.Applied,
		RecreateRequired: resp.TeardownRequired,
	}

	if len(res.RecreateRequired) == 0 {
		if len(res.Applied) > 0 {
			res.NextAction = "applied"
		} else {
			res.NextAction = "nothing_to_do"
		}
		return 0, res, nil
	}

	res.Flavor = reconcile.RecreateFlavor(res.RecreateRequired)
	// Surface sessions so the caller can show them in the prompt / JSON
	// output. Best-effort; failure here is non-fatal.
	res.Sessions = probeSessions(tr, cfg.Project.VMName)
	res.NextAction = "needs_approval"
	return 0, res, nil
}
