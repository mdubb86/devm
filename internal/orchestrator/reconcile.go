package orchestrator

import (
	"fmt"

	"github.com/mtwaage/devm/internal/sandbox"
	"github.com/mtwaage/devm/internal/schema"
	"gopkg.in/yaml.v3"
)

// snapshotHeader is prepended to every written snapshot so the file
// is self-identifying for humans grepping the VM.
const snapshotHeader = "# devm snapshot — last-applied schema.Config\n"

// RunReconcileInner is the lock-less inner of the reconcile state
// machine. The caller MUST already hold .devm/lock and MUST have
// confirmed the sandbox is running (sb.IsRunning()). This function
// reads the in-VM snapshot (last-applied schema.Config), diffs the
// new cfg against it, applies all BucketLive changes via ApplyLive,
// and reports what remains (recreate-required) without prompting
// or executing recreate.
//
// Snapshot semantics:
//   - Empty snapshot → no prior apply. Treat as identical to new cfg
//     (no diff detected; write fresh snapshot at the end so future
//     reconciles have a baseline to diff against).
//   - Non-empty snapshot → parse as schema.Config; structural diff.
//   - On full success (no recreate-required), write new snapshot.
//   - On partial success (recreate pending), leave old snapshot in
//     place so subsequent reconciles re-detect everything. Live
//     changes are idempotent so re-applying is harmless.
func RunReconcileInner(cfg schema.Config, sb *sandbox.Sandbox, repoRoot string) (ReconcileResult, error) {
	res := ReconcileResult{
		Rendered:     true,
		SandboxState: "running",
	}

	snapStr, err := ReadSnapshot(sb)
	if err != nil {
		return res, fmt.Errorf("read snapshot: %w", err)
	}

	var snapCfg schema.Config
	if snapStr == "" {
		// No baseline — treat as identical to current (zero diff).
		snapCfg = cfg
	} else {
		if err := yaml.Unmarshal([]byte(snapStr), &snapCfg); err != nil {
			return res, fmt.Errorf("parse snapshot: %w", err)
		}
	}

	changes := ComputeAllChanges(snapCfg, cfg)
	for _, c := range changes {
		if c.Bucket() == BucketLive {
			res.Applied = append(res.Applied, c)
		} else {
			res.RecreateRequired = append(res.RecreateRequired, c)
		}
	}
	res.Flavor = RecreateFlavor(changes)

	if len(res.Applied) > 0 {
		if err := ApplyLive(sb, res.Applied, cfg.Project.PortOffset); err != nil {
			return res, fmt.Errorf("apply live: %w", err)
		}
	}

	if len(res.RecreateRequired) > 0 {
		res.NextAction = "needs_approval"
		// Surface sessions so the caller can show them in the prompt
		// / JSON output. Best-effort; failure here is non-fatal.
		if sessions, sErr := sb.Sessions(); sErr == nil {
			res.Sessions = sessions
		}
		return res, nil
	}

	// Full success — write snapshot.
	newSnap, err := yaml.Marshal(cfg)
	if err != nil {
		return res, fmt.Errorf("marshal new snapshot: %w", err)
	}
	if err := WriteSnapshot(sb, snapshotHeader+string(newSnap)); err != nil {
		return res, fmt.Errorf("write snapshot: %w", err)
	}

	if len(res.Applied) > 0 {
		res.NextAction = "applied"
	} else {
		res.NextAction = "nothing_to_do"
	}
	return res, nil
}
