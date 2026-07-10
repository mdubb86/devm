package orchestrator

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/mdubb86/devm/internal/lock"
	"github.com/mdubb86/devm/internal/render"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/serviceapi"
	"gopkg.in/yaml.v3"
)

// snapshotHeader is prepended to every written snapshot so the file
// is self-identifying for humans grepping the VM.
const snapshotHeader = "# devm snapshot — last-applied schema.Config\n"

// vmIsRunning returns true when the named VM appears in tart list with
// Running=true.
func vmIsRunning(tr *tart.Tart, vmName string) bool {
	vms, err := tr.List(context.Background())
	if err != nil {
		return false
	}
	for _, vm := range vms {
		if vm.Name == vmName {
			return vm.Running
		}
	}
	return false
}

// RunReconcileInner is the lock-less inner of the reconcile state
// machine. The caller MUST already hold .devm/lock and MUST have
// confirmed the VM is running. This function reads the in-VM snapshot
// (last-applied schema.Config), diffs the new cfg against it, applies
// all BucketLive changes via ApplyLive, and reports what remains
// (recreate-required) without prompting or executing recreate.
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
func RunReconcileInner(cfg schema.Config, tr *tart.Tart, vmName, repoRoot string) (ReconcileResult, error) {
	res := ReconcileResult{
		Rendered:     true,
		SandboxState: "running",
	}

	snapStr, err := ReadSnapshot(tr, vmName)
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

	changes, err := ComputeAllChanges(snapCfg, cfg, repoRoot)
	if err != nil {
		return res, fmt.Errorf("compute changes: %w", err)
	}
	for _, c := range changes {
		if c.Bucket() == BucketLive {
			res.Applied = append(res.Applied, c)
		} else {
			res.RecreateRequired = append(res.RecreateRequired, c)
		}
	}
	res.Flavor = RecreateFlavor(changes)

	if len(res.Applied) > 0 {
		if err := ApplyLive(tr, vmName, res.Applied, cfg, repoRoot); err != nil {
			return res, fmt.Errorf("apply live: %w", err)
		}
	}

	if len(res.RecreateRequired) > 0 {
		res.NextAction = "needs_approval"
		// Surface sessions so the caller can show them in the prompt
		// / JSON output. Best-effort; failure here is non-fatal.
		res.Sessions = probeSessions(tr, vmName)
		return res, nil
	}

	// Full success — write snapshot.
	newSnap, err := yaml.Marshal(cfg)
	if err != nil {
		return res, fmt.Errorf("marshal new snapshot: %w", err)
	}
	if err := WriteSnapshot(tr, vmName, snapshotHeader+string(newSnap)); err != nil {
		return res, fmt.Errorf("write snapshot: %w", err)
	}

	if len(res.Applied) > 0 {
		res.NextAction = "applied"
	} else {
		res.NextAction = "nothing_to_do"
	}
	return res, nil
}

// ReconcileOptions controls the outer state machine behaviour.
type ReconcileOptions struct {
	DryRun         bool
	Yes            bool
	NonInteractive bool      // true when stdin is not a TTY (set by CLI)
	JSON           bool      // affects only CLI output formatting; outer doesn't print directly
	Stdout         io.Writer // optional; defaults to os.Stdout for interactive prompt
	Stdin          io.Reader // optional; defaults to os.Stdin for prompt response
}

// RunReconcile is the lock-acquiring outer of the reconcile state
// machine. Always renders .devm/ first so file-only consumers see the
// latest output; if the VM is running, runs the diff/apply state
// machine; handles --dry-run, --yes, and non-TTY contexts; executes
// recreate (without relaunching a shell) on approval.
//
// Locking discipline: this function acquires .devm/lock for the
// diff/apply phase and RELEASES it before invoking RunStop (which
// acquires its own lock). The user runs `devm shell` themselves to
// re-enter; we do not relaunch.
//
// Exit codes:
//
//	 0  — clean (applied / nothing_to_do)
//	 1  — user refused at prompt
//	 2  — non-TTY context with recreate-required pending (no --yes)
//	-1  — operational error (lock fail, render fail, RunStop fail)
func RunReconcile(cfg schema.Config, tr *tart.Tart, repoRoot string, opts ReconcileOptions) (int, ReconcileResult, error) {
	vmName := cfg.Project.VMName
	res := ReconcileResult{}

	// 1. Bundle install now happens inside the daemon's provisioner step
	// (installDevmBundle); reconcile no longer pre-renders .devm/ on the
	// host. Tasks 5-7 move the rest of this flow into the daemon.
	res.Rendered = true

	// 2. Acquire lock.
	lockPath := filepath.Join(repoRoot, ".devm", "lock")
	lk, err := lock.Acquire(lockPath)
	if err != nil {
		return -1, res, fmt.Errorf("acquire lock: %w", err)
	}
	released := false
	releaseLock := func() {
		if !released {
			_ = lk.Release()
			released = true
		}
	}
	defer releaseLock()

	// 3. VM state check.
	if !vmIsRunning(tr, vmName) {
		// VM stopped: write the full .devm/ (including template
		// installers) so the next `devm shell` cold start picks up
		// everything. No diff or apply needed.
		if err := render.WriteTemplateInstallers(cfg, repoRoot); err != nil {
			return -1, res, fmt.Errorf("render template installers: %w", err)
		}
		res.SandboxState = "stopped"
		res.NextAction = "nothing_to_do"
		return 0, res, nil
	}
	res.SandboxState = "running"

	// 4. Dry-run branch: compute diff without applying or writing snapshot.
	if opts.DryRun {
		snapStr, err := ReadSnapshot(tr, vmName)
		if err != nil {
			return -1, res, fmt.Errorf("read snapshot: %w", err)
		}
		var snapCfg schema.Config
		if snapStr == "" {
			snapCfg = cfg
		} else {
			if err := yaml.Unmarshal([]byte(snapStr), &snapCfg); err != nil {
				return -1, res, fmt.Errorf("parse snapshot: %w", err)
			}
		}
		changes, err := ComputeAllChanges(snapCfg, cfg, repoRoot)
		if err != nil {
			return -1, res, fmt.Errorf("compute changes: %w", err)
		}
		for _, c := range changes {
			if c.Bucket() == BucketLive {
				res.Applied = append(res.Applied, c)
			} else {
				res.RecreateRequired = append(res.RecreateRequired, c)
			}
		}
		res.Flavor = RecreateFlavor(changes)
		switch {
		case len(res.RecreateRequired) > 0:
			res.NextAction = "needs_approval"
		case len(res.Applied) > 0:
			res.NextAction = "applied"
		default:
			res.NextAction = "nothing_to_do"
		}
		return 0, res, nil
	}

	// 5. Real apply via Inner.
	inner, err := RunReconcileInner(cfg, tr, vmName, repoRoot)
	if err != nil {
		// Surface whatever partial state Inner gathered.
		res.Applied = inner.Applied
		res.RecreateRequired = inner.RecreateRequired
		res.Flavor = inner.Flavor
		res.Sessions = inner.Sessions
		res.NextAction = inner.NextAction
		return -1, res, err
	}
	res.Applied = inner.Applied
	res.RecreateRequired = inner.RecreateRequired
	res.Flavor = inner.Flavor
	res.Sessions = inner.Sessions
	res.NextAction = inner.NextAction

	// 6. No recreate? Done.
	if len(res.RecreateRequired) == 0 {
		return 0, res, nil
	}

	// 7. Recreate-required path: handle approval.
	if !opts.Yes {
		if opts.NonInteractive {
			return 2, res, nil
		}
		// Interactive prompt.
		stdout := opts.Stdout
		if stdout == nil {
			stdout = os.Stdout
		}
		stdin := opts.Stdin
		if stdin == nil {
			stdin = os.Stdin
		}
		fmt.Fprint(stdout, FormatReconcileText(res))
		fmt.Fprint(stdout, "[y/N]: ")
		var resp string
		_, _ = fmt.Fscanln(stdin, &resp)
		if resp != "y" && resp != "Y" {
			res.NextAction = "user_refused"
			return 1, res, nil
		}
	}

	// 8. Execute recreate. Release our lock first so RunStop can acquire.
	releaseLock()

	stopDeps := StopDeps{
		Tart:             tr,
		ServiceAPIClient: serviceapi.NewClient(),
		LockPath:         lockPath,
	}
	mode := StopPreserve
	if res.Flavor == FlavorTeardownShell {
		mode = StopDestroy
	}
	if _, err := RunStop(context.Background(), stopDeps, cfg.Project.ID, vmName, mode, true); err != nil {
		return -1, res, fmt.Errorf("recreate (%s): %w", res.Flavor, err)
	}

	res.NextAction = "applied"
	return 0, res, nil
}
