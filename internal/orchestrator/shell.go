package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mtwaage/devm/internal/debuglog"
	"github.com/mtwaage/devm/internal/lock"
	"github.com/mtwaage/devm/internal/render"
	"github.com/mtwaage/devm/internal/sandbox"
	"github.com/mtwaage/devm/internal/schema"
	"gopkg.in/yaml.v3"
)

// ShellDeps wires the orchestrator's collaborators. Production callers
// build one via DefaultShellDeps; tests substitute stubs.
type ShellDeps struct {
	// AnchorSpawner runs the `sbx run` subprocess that holds the
	// sandbox alive during bootstrap. Non-interactive: stdin=/dev/null,
	// stdout/stderr typically discarded or logged.
	AnchorSpawner Spawner
	// UserSpawner runs the user's interactive shell via sbx exec -it.
	// Must inherit the host's stdin/stdout/stderr so the user sees
	// their shell.
	UserSpawner Spawner
	Runner      sandbox.Runner

	LockPath       string
	WaitForRunning time.Duration // default 60s
	WaitForPty     time.Duration // legacy settle delay; unused on the anchor-alive path. default 500ms
	PollInterval   time.Duration // default 250ms
}

// DefaultShellDeps returns deps wired for production.
func DefaultShellDeps(repoRoot string) ShellDeps {
	return ShellDeps{
		AnchorSpawner:  &ExecSpawner{Interactive: false},
		UserSpawner:    &ExecSpawner{Interactive: true},
		Runner:         sandbox.DefaultRunner{},
		LockPath:       filepath.Join(repoRoot, ".devm", "lock"),
		WaitForRunning: 60 * time.Second,
		WaitForPty:     500 * time.Millisecond,
		PollInterval:   250 * time.Millisecond,
	}
}

// SAFETY INVARIANT — anchor handoff
//
// sbx's "sandbox is running" state is driven by its session count
// (the number of attached pty sessions: sbx run + sbx exec -it). When
// the count drops to 0, sbx stops the container — killing all
// in-VM processes, including the user's shell.
//
// On cold start we use TWO sessions transiently:
//   1. sbx run subprocess (the anchor) — gives us a session while we
//      do setup (waitForRunning, port reconcile).
//   2. sbx exec -it bash — the user's actual shell.
//
// We MUST add session #2 BEFORE removing session #1. The orchestrator
// does this in order:
//   a. Spawn sbx run subprocess.
//   b. Wait for sandbox running.
//   c. Port reconcile.
//   d. Spawn sbx exec -it bash (user shell).
//   e. Brief settle delay so the host-side sbx exec has time to
//      register its session with the sbx daemon.
//   f. killRun: kill the sbx run subprocess on host.
//   g. Post-handoff check: confirm sandbox is still running (user
//      session must be holding it alive). If not, the handoff failed.
//
// Between (a) and (f) the session count is ≥ 1. Between (d) and (f)
// it is 2. After (f) it is 1 (user shell only). Sessions never reach 0
// until the user exits.

// RunShell implements `devm shell`. Returns the user shell's exit code
// and a non-nil error only when an orchestration step itself failed
// (lock acquire, sbx run timeout, port reconcile error, user shell
// spawn error).
func RunShell(ctx context.Context, d ShellDeps, cfg schema.Config, repoRoot, sandboxName, cmdName string, cmdArgs []string) (int, error) {
	applyDefaults(&d)

	lk, err := lock.Acquire(d.LockPath)
	if err != nil {
		return -1, fmt.Errorf("acquire lock: %w", err)
	}
	released := false
	defer func() {
		if !released {
			_ = lk.Release()
		}
	}()

	// Render .devm/ from the current devm.yaml before doing anything.
	// Cold start feeds .devm/spec.yaml to `sbx run`, so a stale cache
	// would silently launch the sandbox with old config. Rendering here
	// removes the "edit devm.yaml then devm shell uses old config unless
	// you reconcile first" foot-gun. Cheap and idempotent.
	if err := render.WriteDevmDir(cfg, repoRoot); err != nil {
		return -1, fmt.Errorf("render devm dir: %w", err)
	}

	sb := &sandbox.Sandbox{Name: sandboxName, Runner: d.Runner}

	if sb.IsRunning() {
		// Auto-apply LIVE changes before attaching. We already hold the
		// lock, so use the lock-less inner. If recreate is needed,
		// surface to stderr but proceed to attach (stdout is reserved
		// for the user shell). `devm reconcile --yes` is the channel
		// for actually applying recreate-required changes.
		if inner, err := RunReconcileInner(cfg, sb, repoRoot); err != nil {
			fmt.Fprintf(os.Stderr, "warning: reconcile during attach failed: %v\n", err)
		} else if len(inner.RecreateRequired) > 0 {
			fmt.Fprint(os.Stderr, FormatReconcileText(inner))
			fmt.Fprintln(os.Stderr, "(Run `devm reconcile --yes` to apply these — your shell will be restarted.)")
		}
		_ = lk.Release()
		released = true
		return runUserShell(d, cfg, sandboxName, cmdName, cmdArgs)
	}

	// Cold start.
	// sbx run's invocation differs depending on whether the sandbox
	// already exists.
	//
	//   Create:  sbx run --kit <dir> --name <name> <agent> <workspace>
	//   Restart: sbx run --kit <dir> <name>  (--kit so sbx can resolve
	//     our custom agent definition; sbx doesn't remember it across
	//     restarts. NO --name — sbx rejects it for existing sandboxes.)
	//
	// We branch on Exists() since IsRunning() was already false above
	// (we wouldn't reach this code path if the sandbox were running).
	// Anchor must ignore SIGHUP so the sandbox survives when the user's
	// terminal closes (kernel sends SIGHUP to processes holding the
	// closing PTY as their controlling tty; default action is
	// terminate). Wrapping in `nohup` is the portable way to get the
	// child to inherit SIG_IGN through the exec. POSIX nohup execvps
	// its argument list, so runCmd.Pid() points to the resulting
	// `sbx run` process (not `nohup`). Pinned empirically by
	// e2e/test_sbx_anchor_10_terminal_close.py shape `ignhup_only`.
	var runArgs []string
	if sb.Exists() {
		// Restart of existing sandbox: include --kit so sbx can resolve
		// our custom kit's agent definition (sbx doesn't remember it
		// across restarts). DO NOT pass --name here; sbx rejects it for
		// existing sandboxes.
		runArgs = []string{
			"sbx", "run",
			"--kit", filepath.Join(repoRoot, ".devm"),
			sandboxName,
		}
	} else {
		runArgs = []string{
			"sbx", "run",
			"--kit", filepath.Join(repoRoot, ".devm"),
			"--name", sandboxName,
			cfg.Project.ID,
			repoRoot,
		}
	}
	debuglog.Logf("shell", "cold-start: spawning anchor: nohup %v", runArgs)
	runCmd, err := d.AnchorSpawner.Start("nohup", runArgs...)
	if err != nil {
		return -1, fmt.Errorf("spawn sbx run: %w", err)
	}
	debuglog.Logf("shell", "cold-start: anchor spawned pid=%d", runCmd.Pid())
	runDone := make(chan error, 1)
	go func() {
		_, err := runCmd.Wait()
		runDone <- err
	}()

	// Cleanup helper for failure paths. The normal path never calls this.
	killAnchor := func() {
		debuglog.Logf("shell", "cleanup: killing anchor pid=%d", runCmd.Pid())
		_ = runCmd.Kill()
		select {
		case <-runDone:
		case <-time.After(3 * time.Second):
		}
	}

	if err := waitForRunning(ctx, sb, runDone, d.WaitForRunning, d.PollInterval); err != nil {
		killAnchor()
		return -1, err
	}
	debuglog.Logf("shell", "cold-start: sandbox status=running")

	if err := waitForExecReady(sb, d.Runner, 30*time.Second); err != nil {
		killAnchor()
		return -1, fmt.Errorf("sandbox readiness: %w", err)
	}
	debuglog.Logf("shell", "cold-start: exec-ready")

	// Port reconcile and snapshot BEFORE user shell. The anchor is
	// alive, holding the session — publishes stick immediately and
	// without the phantom/vanish dance the old flow had to defend
	// against (Quirk #2, #3 in docs/sbx-quirks.md). Pinned by
	// e2e/test_sbx_anchor_05_publish_sticks.py and
	// e2e/test_sbx_anchor_09_port_cycle.py.
	debuglog.Logf("shell", "port-reconcile: starting")
	if err := ReconcilePortsWithRunner(sb, cfg, d.Runner); err != nil {
		fmt.Fprintf(os.Stderr, "warning: port reconcile failed: %v\n", err)
	}
	debuglog.Logf("shell", "port-reconcile: done")

	debuglog.Logf("shell", "snapshot: writing")
	if snapYAML, mErr := yaml.Marshal(cfg); mErr == nil {
		_ = WriteSnapshot(sb, snapshotHeader+string(snapYAML))
	}
	debuglog.Logf("shell", "snapshot: done")

	// Spawn the user's interactive shell. UserSpawner inherits the host
	// terminal's stdin/stdout/stderr, so the user shell ends up in the
	// same session as devm (which is in the same session as the
	// anchor — Go exec.Cmd default). Same-session is required for
	// daemon survival under sbx (see Quirk #5).
	execArgs := []string{"exec", "-it"}
	execArgs = append(execArgs, sandbox.EnvArgs(cfg)...)
	execArgs = append(execArgs, sandboxName, cmdName)
	execArgs = append(execArgs, cmdArgs...)
	debuglog.Logf("shell", "spawning user shell: sbx %v", execArgs)
	userCmd, err := d.UserSpawner.Start("sbx", execArgs...)
	if err != nil {
		killAnchor()
		return -1, fmt.Errorf("spawn user shell: %w", err)
	}
	debuglog.Logf("shell", "user shell spawned pid=%d", userCmd.Pid())

	_ = lk.Release()
	released = true

	rc, err := userCmd.Wait()
	if err != nil {
		return -1, fmt.Errorf("user shell wait: %w", err)
	}
	debuglog.Logf("shell", "user shell exited rc=%d; leaving anchor pid=%d alive", rc, runCmd.Pid())
	return rc, nil
}

func applyDefaults(d *ShellDeps) {
	if d.WaitForRunning == 0 {
		d.WaitForRunning = 60 * time.Second
	}
	if d.WaitForPty == 0 {
		d.WaitForPty = 500 * time.Millisecond
	}
	if d.PollInterval == 0 {
		d.PollInterval = 250 * time.Millisecond
	}
}

func waitForRunning(ctx context.Context, sb *sandbox.Sandbox, runDone <-chan error, timeout, poll time.Duration) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		if sb.IsRunning() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-runDone:
			if err != nil {
				return fmt.Errorf("sbx run exited before sandbox became running: %w", err)
			}
			return fmt.Errorf("sbx run exited before sandbox became running")
		case <-ticker.C:
			if time.Now().After(deadline) {
				return fmt.Errorf("sandbox %s never reached running within %s", sb.Name, timeout)
			}
		}
	}
}

// runUserShell is the already-running shortcut: just attach to the
// existing sandbox. We deliberately skip port reconcile here because
// the sandbox came up via an earlier devm shell that already ran
// reconcile; rerunning would be cheap but adds a noticeable startup
// delay for the common case. If devm.yaml ports have changed since
// the last cold start, the user must `devm stop` and re-shell.
func runUserShell(d ShellDeps, cfg schema.Config, sandboxName, cmdName string, cmdArgs []string) (int, error) {
	args := []string{"exec", "-it"}
	args = append(args, sandbox.EnvArgs(cfg)...)
	args = append(args, sandboxName, cmdName)
	args = append(args, cmdArgs...)
	cmd, err := d.UserSpawner.Start("sbx", args...)
	if err != nil {
		return -1, err
	}
	rc, err := cmd.Wait()
	if err != nil {
		return -1, fmt.Errorf("user shell wait: %w", err)
	}
	return rc, nil
}
