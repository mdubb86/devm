package orchestrator

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/mtwaage/devm/internal/lock"
	"github.com/mtwaage/devm/internal/sandbox"
	"github.com/mtwaage/devm/internal/schema"
)

// ShellDeps wires the orchestrator's collaborators. Production callers
// build one via DefaultShellDeps; tests substitute stubs.
type ShellDeps struct {
	Spawner Spawner
	Runner  sandbox.Runner

	LockPath       string
	WaitForRunning time.Duration // default 60s
	WaitForPty     time.Duration // default 5s
	PollInterval   time.Duration // default 250ms
}

// DefaultShellDeps returns deps wired for production. The Spawner is
// configured for non-interactive use (the sbx run anchor subprocess);
// the user shell uses its own ExecSpawner internally.
func DefaultShellDeps(repoRoot string) ShellDeps {
	return ShellDeps{
		Spawner:        &ExecSpawner{Interactive: false},
		Runner:         sandbox.DefaultRunner{},
		LockPath:       filepath.Join(repoRoot, ".devm", "lock"),
		WaitForRunning: 60 * time.Second,
		WaitForPty:     5 * time.Second,
		PollInterval:   250 * time.Millisecond,
	}
}

// RunShell implements `devm shell`. Returns the user shell's exit code
// (0 on clean exit, 1 on non-zero) and a non-nil error only when an
// orchestration step itself failed (lock acquire, sbx run timeout,
// port reconcile error, user shell spawn error).
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

	if isRunning(sb, d.Runner) {
		// Shortcut: just attach via the configured spawner.
		_ = lk.Release()
		released = true
		return runUserShell(d, sandboxName, cmdName, cmdArgs)
	}

	// Cold start.
	runArgs := []string{"run", "--kit", filepath.Join(repoRoot, ".devm"), sandboxName, repoRoot}
	runCmd, err := d.Spawner.Start("sbx", runArgs...)
	if err != nil {
		return -1, fmt.Errorf("spawn sbx run: %w", err)
	}
	runDone := make(chan error, 1)
	go func() { runDone <- runCmd.Wait() }()

	// Cleanup helper for failure paths.
	killRun := func() {
		_ = runCmd.Kill()
		select {
		case <-runDone:
		case <-time.After(3 * time.Second):
		}
	}

	if err := waitForRunning(ctx, sb, d.Runner, d.WaitForRunning, d.PollInterval); err != nil {
		killRun()
		return -1, err
	}

	if err := ReconcilePortsWithRunner(sb, cfg, d.Runner); err != nil {
		killRun()
		return -1, err
	}

	// Spawn user's interactive shell via the same Spawner. Production
	// callers pass a Spawner that ignores the Interactive flag set at
	// construction; for the user shell we want terminal inheritance.
	// We achieve that by always going through d.Spawner here — in tests
	// the stub doesn't care; in production the caller arranges the
	// right spawner for this call site (see cmd/devm/shell.go).
	execArgs := append([]string{"exec", "-it", sandboxName, cmdName}, cmdArgs...)
	userCmd, err := d.Spawner.Start("sbx", execArgs...)
	if err != nil {
		killRun()
		return -1, fmt.Errorf("spawn user shell: %w", err)
	}

	if err := waitForUserPty(ctx, sb, d.Runner, d.WaitForPty, d.PollInterval); err != nil {
		_ = userCmd.Kill()
		killRun()
		return -1, err
	}

	// Anchor's job is done — kill it. User pty keeps the sandbox alive.
	killRun()

	_ = lk.Release()
	released = true

	if err := userCmd.Wait(); err != nil {
		return 1, nil
	}
	return 0, nil
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

// isRunning returns true when `sbx ls` reports the sandbox in running
// state. Tolerant of unknown output formats: returns false on parse
// failure or any unrelated content.
func isRunning(sb *sandbox.Sandbox, r sandbox.Runner) bool {
	out, err := r.Output("sbx", "ls")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == sb.Name && strings.Contains(line, "running") {
			return true
		}
	}
	return false
}

func waitForRunning(ctx context.Context, sb *sandbox.Sandbox, r sandbox.Runner, timeout, poll time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if isRunning(sb, r) {
			return nil
		}
		time.Sleep(poll)
	}
	return fmt.Errorf("sandbox %s never reached running within %s", sb.Name, timeout)
}

func waitForUserPty(ctx context.Context, sb *sandbox.Sandbox, r sandbox.Runner, timeout, poll time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		sessions, err := sb.SessionsWithRunner(r)
		if err == nil && len(sessions) > 0 {
			return nil
		}
		time.Sleep(poll)
	}
	return fmt.Errorf("user shell pty never appeared within %s", timeout)
}

// runUserShell is the already-running shortcut: just spawn the user
// shell via the configured spawner and wait.
func runUserShell(d ShellDeps, sandboxName, cmdName string, cmdArgs []string) (int, error) {
	args := append([]string{"exec", "-it", sandboxName, cmdName}, cmdArgs...)
	cmd, err := d.Spawner.Start("sbx", args...)
	if err != nil {
		return -1, err
	}
	if err := cmd.Wait(); err != nil {
		return 1, nil
	}
	return 0, nil
}
