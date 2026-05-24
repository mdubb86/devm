package orchestrator

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/mtwaage/devm/internal/lock"
	"github.com/mtwaage/devm/internal/sandbox"
	"github.com/mtwaage/devm/internal/schema"
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
	WaitForPty     time.Duration // default 5s
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
		WaitForPty:     5 * time.Second,
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
//   e. waitForUserPty: poll sb.Sessions() until user pty exists.
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

	sb := &sandbox.Sandbox{Name: sandboxName, Runner: d.Runner}

	if sb.IsRunning() {
		// Shortcut: just attach via the user spawner.
		_ = lk.Release()
		released = true
		return runUserShell(d, sandboxName, cmdName, cmdArgs)
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
	var runArgs []string
	if sb.Exists() {
		// Restart of existing sandbox: include --kit so sbx can resolve
		// our custom kit's agent definition (sbx doesn't remember it
		// across restarts). DO NOT pass --name here; sbx rejects it for
		// existing sandboxes.
		runArgs = []string{
			"run",
			"--kit", filepath.Join(repoRoot, ".devm"),
			sandboxName,
		}
	} else {
		runArgs = []string{
			"run",
			"--kit", filepath.Join(repoRoot, ".devm"),
			"--name", sandboxName,
			cfg.Project.ID,
			repoRoot,
		}
	}
	runCmd, err := d.AnchorSpawner.Start("sbx", runArgs...)
	if err != nil {
		return -1, fmt.Errorf("spawn sbx run: %w", err)
	}
	runDone := make(chan error, 1)
	go func() {
		_, err := runCmd.Wait()
		runDone <- err
	}()

	// Cleanup helper for failure paths.
	killRun := func() {
		_ = runCmd.Kill()
		select {
		case <-runDone:
		case <-time.After(3 * time.Second):
		}
	}

	if err := waitForRunning(ctx, sb, runDone, d.WaitForRunning, d.PollInterval); err != nil {
		killRun()
		return -1, err
	}

	if err := ReconcilePortsWithRunner(sb, cfg, d.Runner); err != nil {
		killRun()
		return -1, err
	}

	// Spawn the user's interactive shell. The UserSpawner is configured
	// to inherit the host terminal's stdin/stdout/stderr.
	execArgs := append([]string{"exec", "-it", sandboxName, cmdName}, cmdArgs...)
	userCmd, err := d.UserSpawner.Start("sbx", execArgs...)
	if err != nil {
		killRun()
		return -1, fmt.Errorf("spawn user shell: %w", err)
	}

	// Reap userCmd in the background so a failure during waitForUserPty
	// (or anywhere before the final wait) doesn't leak the process.
	type userResult struct {
		rc  int
		err error
	}
	userDone := make(chan userResult, 1)
	go func() {
		rc, err := userCmd.Wait()
		userDone <- userResult{rc: rc, err: err}
	}()

	if err := waitForUserPty(ctx, sb, runDone, d.WaitForPty, d.PollInterval); err != nil {
		_ = userCmd.Kill()
		select {
		case <-userDone:
		case <-time.After(3 * time.Second):
		}
		killRun()
		return -1, err
	}

	// Anchor's job is done. Killing the sbx run subprocess causes sbx
	// daemon to clean up the in-VM entrypoint process (shell-wrapped
	// entrypoint is required for this cleanup to actually propagate;
	// see internal/render/spec.go entrypoint comment).
	killRun()

	// Safety invariant: with sbx run anchor gone, the user shell's pty
	// must be the only thing keeping the sandbox alive. If the sandbox
	// is NOT running here, the handoff failed (session count dropped to
	// 0). The user's shell session would also be dead at this point.
	if !sb.IsRunning() {
		_ = userCmd.Kill()
		select {
		case <-userDone:
		case <-time.After(3 * time.Second):
		}
		return -1, fmt.Errorf("safety invariant violated: sandbox stopped during anchor cleanup; user session not preserved")
	}

	_ = lk.Release()
	released = true

	res := <-userDone
	if res.err != nil {
		return -1, fmt.Errorf("user shell wait: %w", res.err)
	}
	return res.rc, nil
}

func applyDefaults(d *ShellDeps) {
	if d.WaitForRunning == 0 {
		d.WaitForRunning = 60 * time.Second
	}
	if d.WaitForPty == 0 {
		d.WaitForPty = 5 * time.Second
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

func waitForUserPty(ctx context.Context, sb *sandbox.Sandbox, runDone <-chan error, timeout, poll time.Duration) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		sessions, err := sb.Sessions()
		if err == nil && len(sessions) > 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-runDone:
			if err != nil {
				return fmt.Errorf("sbx run exited before user pty appeared: %w", err)
			}
			return fmt.Errorf("sbx run exited before user pty appeared")
		case <-ticker.C:
			if time.Now().After(deadline) {
				return fmt.Errorf("user shell pty never appeared within %s", timeout)
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
func runUserShell(d ShellDeps, sandboxName, cmdName string, cmdArgs []string) (int, error) {
	args := append([]string{"exec", "-it", sandboxName, cmdName}, cmdArgs...)
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
