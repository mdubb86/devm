package orchestrator

import (
	"context"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/creack/pty"
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
	// AnchorSpawner is an OPTIONAL hook for tests to intercept anchor
	// (`sbx run …`) spawning. Production code leaves this nil
	// and goes through a raw *exec.Cmd path inline in RunShell — the
	// SpawnedCmd interface indirection on the anchor (combined with
	// the ticker+select waitForRunning loop) was Quirk #6: it
	// destabilized the sandbox endpoint and caused the subsequent
	// port publish to phantom in ~80% of cold starts. See
	// docs/sbx-quirks.md section 6 and e2e/test_sbx_anchor_14_*.py
	// for the pin. Test injects a stubSpawner here to verify
	// orchestration behavior without spawning real subprocesses.
	AnchorSpawner Spawner
	// UserSpawner runs the user's interactive shell via sbx exec -it.
	// Must inherit the host's stdin/stdout/stderr so the user sees
	// their shell.
	UserSpawner Spawner
	Runner      sandbox.Runner

	LockPath       string
	WaitForRunning time.Duration // default 60s
	PollInterval   time.Duration // default 250ms
}

// DefaultShellDeps returns deps wired for production. AnchorSpawner is
// left nil so RunShell takes the raw-*exec.Cmd anchor path (see Quirk
// #6 in docs/sbx-quirks.md).
func DefaultShellDeps(repoRoot string) ShellDeps {
	return ShellDeps{
		AnchorSpawner:  nil,
		UserSpawner:    &ExecSpawner{Interactive: true},
		Runner:         sandbox.DefaultRunner{},
		LockPath:       filepath.Join(repoRoot, ".devm", "lock"),
		WaitForRunning: 60 * time.Second,
		PollInterval:   250 * time.Millisecond,
	}
}

// ANCHOR LIFECYCLE (anchor-alive design)
//
// `sbx run` is the anchor: a host-side process that holds an open
// sbx daemon session for the sandbox. As long as the anchor is
// alive, the sandbox stays running.
//
// devm spawns the anchor once at cold-start under a PTY (via
// pty.StartWithSize). sbx 0.31 ignores SIGHUP when running with a
// controlling TTY (TUI-style signal handling), so the sandbox
// survives a terminal-close cascade — devm exiting closes the PTY
// master, but the anchor absorbs the resulting SIGHUP. The next
// `devm shell` from a new terminal warm-paths into the still-
// running sandbox. Pinned by
// e2e/test_sbx_interop_02_anchor_master_close_lifetime.py.
//
// devm NEVER kills the anchor on the normal path. The anchor exits
// on its own when:
//   * `devm stop` / `devm teardown` run `sbx stop NAME` / `sbx rm NAME`.
//     Verified by e2e/test_sbx_anchor_04_sbx_stop_reaps_anchor.py.
//   * The user runs `sbx stop NAME` directly.
//
// All of the orchestration order this code used to gate on (settle
// delay, killRun, post-kill safety check, "port reconcile must wait
// for anchor death") is gone. Port reconcile and snapshot happen
// any time while the anchor is alive; publish sticks under the
// live session (e2e/test_sbx_anchor_05_publish_sticks.py).
//
// Failure paths still call killAnchor() for cleanup, but the
// normal path returns with the anchor running.
//
// See docs/sbx-quirks.md section 5 for the empirical backing on
// why anchor-alive is required (the 5s daemon kill triggered by
// anchor death) and "Refinement: anchor must ignore SIGHUP" for
// the terminal-close cascade.

// RunShell implements `devm shell`. Returns the user shell's exit code
// and a non-nil error only when an orchestration step itself failed
// (lock acquire, sbx run timeout, port reconcile error, user shell
// spawn error).
func RunShell(ctx context.Context, d ShellDeps, cfg schema.Config, repoRoot, sandboxName, cmdName string, cmdArgs []string) (rc int, retErr error) {
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
		//
		// Reconcile failure is FATAL here too — silently dropping into
		// a shell with half-applied LIVE changes hides whatever is
		// actually wrong with the user's devm.yaml. We don't kill the
		// anchor: it's from a previous session and config bugs
		// shouldn't cost the user their running sandbox.
		inner, err := RunReconcileInner(cfg, sb, repoRoot)
		if err != nil {
			return -1, fmt.Errorf("reconcile during attach failed: %w", err)
		}
		if len(inner.RecreateRequired) > 0 {
			fmt.Fprint(os.Stderr, FormatReconcileText(inner))
			fmt.Fprintln(os.Stderr, "(Run `devm reconcile --yes` to apply these — your shell will be restarted.)")
		}
		_ = lk.Release()
		released = true
		return runUserShell(d, cfg, repoRoot, sandboxName, cmdName, cmdArgs)
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
	//
	// No nohup wrap: sbx 0.31 (post Tier 1c PTY) ignores SIGHUP when
	// running under a controlling TTY. Closing the PTY master from
	// devm doesn't kill the anchor. Pinned by
	// e2e/test_sbx_interop_02_anchor_master_close_lifetime.py.
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
		// Append additional mounts from cfg.Mounts. Each entry is
		// resolved to an absolute path (with ~ expansion and projectRoot
		// for relatives), preserving the optional :ro suffix. Sbx mounts
		// each at the same path inside the VM ("mirrored path" mode).
		// Mount changes are TEARDOWN-bucket: positional workspaces are
		// baked at create time and the sandbox must be rm'd to apply a
		// changed mounts list.
		for i, entry := range cfg.Mounts {
			resolved, err := schema.ResolveMount(entry, repoRoot)
			if err != nil {
				return -1, fmt.Errorf("mounts[%d]: %w", i, err)
			}
			runArgs = append(runArgs, resolved)
		}
	}
	// Spawn the anchor.
	//
	// PRODUCTION PATH: d.AnchorSpawner is nil (see DefaultShellDeps).
	// We use a raw *exec.Cmd inline. The SpawnedCmd interface
	// indirection — combined with the ticker+select loop in the old
	// waitForRunning helper — was Quirk #6: the sandbox endpoint
	// became unstable and the subsequent port publish phantomed in
	// ~80% of cold starts. Empirically 10/10 publish-OK with the
	// raw-osexec + inline-poll combination (vs 8/10 with only inline-
	// poll and ~2/10 baseline). See docs/sbx-quirks.md section 6 and
	// e2e/test_sbx_anchor_14_*.py for the pin.
	//
	// TEST PATH: d.AnchorSpawner is a stub; tests use it to verify
	// orchestration without spawning real subprocesses. The stub is
	// not subject to Quirk #6 (it doesn't create real *exec.Cmd).
	var (
		runCmd     *osexec.Cmd      // production path only
		stubAnchor SpawnedCmd       // test path only
		anchorOut  *lineRingBuffer  // production path only — capture for diagnostics
		anchorPid  int
		runDone    = make(chan error, 1)
	)
	if d.AnchorSpawner != nil {
		debuglog.Logf("shell", "cold-start: spawning anchor (via AnchorSpawner — TEST PATH): %v", runArgs)
		sc, err := d.AnchorSpawner.Start(runArgs[0], runArgs[1:]...)
		if err != nil {
			return -1, fmt.Errorf("spawn sbx run: %w", err)
		}
		stubAnchor = sc
		anchorPid = sc.Pid()
		go func() {
			_, err := sc.Wait()
			runDone <- err
		}()
	} else {
		debuglog.Logf("shell", "cold-start: spawning anchor (PTY — production path): %v", runArgs)
		runCmd = osexec.Command(runArgs[0], runArgs[1:]...)
		anchorOut = newLineRingBuffer(200)
		// PTY anchor: sbx writes diagnostic output only under TTY. By giving
		// sbx run a PTY for its stdio, the anchor ring buffer captures the
		// sbx output a user would see in a terminal — error messages,
		// progress, status — and the existing failure-path decorator
		// (formatAnchorOutput) surfaces it on cold-start errors.
		//
		// Size is set to 24x80 explicitly so sbx doesn't see a 0x0 PTY
		// (some CLIs degrade silently in that case).
		ptmx, err := pty.StartWithSize(runCmd, &pty.Winsize{Rows: 24, Cols: 80})
		if err != nil {
			return -1, fmt.Errorf("spawn sbx run (PTY): %w", err)
		}
		// Drain the PTY master into the ring buffer until sbx exits.
		go func() {
			_, _ = io.Copy(anchorOut, ptmx)
			_ = ptmx.Close()
		}()
		if runCmd.Process != nil {
			anchorPid = runCmd.Process.Pid
		}
		go func() {
			runDone <- runCmd.Wait()
		}()
	}
	debuglog.Logf("shell", "cold-start: anchor spawned pid=%d", anchorPid)

	// Single failure-cleanup point. Any error between "anchor started"
	// and "user shell handed off" should tear down the anchor and
	// return — no half-state sandboxes left running. Instead of calling
	// killAnchor() at every error site, we install one defer that
	// fires unless `handedOff` flips true at the final hand-off.
	//
	// The defer also augments the returned error with the anchor's
	// captured stdout/stderr (last 200 lines from the ring buffer).
	// That makes EVERY cold-start failure — port reconcile, snapshot,
	// user-shell-spawn — surface what sbx was actually doing/failing
	// at, not just devm's wrapper text. Previously the anchor tail
	// was only included on the runDone-died path, which left genuine
	// diagnostic gaps (the 2026-06-05 dogfood "no sandbox named
	// 'everstone'" failures gave us zero clue what install: was up to).
	handedOff := false
	defer func() {
		if handedOff {
			return
		}
		debuglog.Logf("shell", "cleanup: killing anchor pid=%d", anchorPid)
		switch {
		case runCmd != nil && runCmd.Process != nil:
			_ = runCmd.Process.Kill()
		case stubAnchor != nil:
			_ = stubAnchor.Kill()
		}
		select {
		case <-runDone:
		case <-time.After(3 * time.Second):
		}
		// Decorate the returned error with the anchor's captured
		// output. Named return `retErr` is mutable from inside this
		// defer, so subsequent error sites don't need to remember to
		// fold the tail in themselves.
		if retErr != nil && anchorOut != nil {
			if tail := formatAnchorOutput(anchorOut); tail != "" {
				retErr = fmt.Errorf("%w%s", retErr, tail)
			}
		}
	}()

	// Wait for the sandbox to reach "running" via an idiomatic
	// ticker+select loop. Pre-sbx-0.31 this needed the inline poll +
	// non-blocking default shape to dodge Quirk #6 (publish phantom
	// triggered by the blocking-select-with-ticker pattern around the
	// anchor spawn). sbx 0.31 fixed Quirk #6, pinned by
	// e2e/test_sbx_anchor_14_publish_trigger_pin.py.
	{
		ticker := time.NewTicker(d.PollInterval)
		defer ticker.Stop()
		deadline := time.After(d.WaitForRunning)
	wait:
		for {
			if sb.IsRunning() {
				break wait
			}
			select {
			case err := <-runDone:
				// Anchor died on its own — defer's Kill is a no-op,
				// but its drain of runDone would block forever on the
				// already-consumed channel. Flip handedOff so the
				// defer skips cleanup entirely. (The defer's
				// error-decoration won't run either when handedOff is
				// true, so fold the anchor tail in here manually.)
				handedOff = true
				tail := formatAnchorOutput(anchorOut)
				if err != nil {
					return -1, fmt.Errorf("sbx run exited before sandbox became running: %w%s", err, tail)
				}
				return -1, fmt.Errorf("sbx run exited before sandbox became running%s", tail)
			case <-ctx.Done():
				return -1, ctx.Err()
			case <-deadline:
				return -1, fmt.Errorf("sandbox %s never reached running within %s", sandboxName, d.WaitForRunning)
			case <-ticker.C:
			}
		}
	}
	debuglog.Logf("shell", "cold-start: sandbox status=running")

	if err := waitForExecReady(sb, d.Runner, 30*time.Second); err != nil {
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
		return -1, fmt.Errorf("port reconcile failed: %w", err)
	}
	debuglog.Logf("shell", "port-reconcile: done")

	debuglog.Logf("shell", "snapshot: writing")
	// Snapshot is the persisted "last-applied" config that the next
	// reconcile diffs against. Dropping it silently produces a sandbox
	// whose next reconcile shows false-positive diffs — surface and
	// let the defer tear down.
	snapYAML, mErr := yaml.Marshal(cfg)
	if mErr != nil {
		return -1, fmt.Errorf("marshal snapshot: %w", mErr)
	}
	if err := WriteSnapshot(sb, snapshotHeader+string(snapYAML)); err != nil {
		return -1, fmt.Errorf("write snapshot: %w", err)
	}
	debuglog.Logf("shell", "snapshot: done")

	// Spawn the user's interactive shell. UserSpawner inherits the host
	// terminal's stdin/stdout/stderr, so the user shell ends up in the
	// same session as devm (which is in the same session as the
	// anchor — Go exec.Cmd default). Same-session is required for
	// daemon survival under sbx (see docs/sbx-quirks.md section 5).
	// Prepend the with-devm-env wrapper so persistent project + service
	// env (and the devm-injected WORKSPACE/IS_SANDBOX) reach the user
	// shell via /.devm/.env. EnvArgs still rides on -e flags for the
	// host-forwarded TERM/COLORTERM/etc. — those vary per terminal.
	wrapper := filepath.Join(repoRoot, ".devm", "scripts", "with-devm-env")
	execArgs := []string{"exec", "-it"}
	execArgs = append(execArgs, sandbox.EnvArgs(cfg)...)
	execArgs = append(execArgs, sandboxName, wrapper, cmdName)
	execArgs = append(execArgs, cmdArgs...)
	debuglog.Logf("shell", "spawning user shell: sbx %v", execArgs)
	userCmd, err := d.UserSpawner.Start("sbx", execArgs...)
	if err != nil {
		return -1, fmt.Errorf("spawn user shell: %w", err)
	}
	// Hand-off complete — anchor now belongs to the lifecycle the
	// user shell holds (anchor-alive contract). The defer above stops
	// being a kill-anchor on the success path.
	handedOff = true
	debuglog.Logf("shell", "user shell spawned pid=%d", userCmd.Pid())

	_ = lk.Release()
	released = true

	var waitErr error
	rc, waitErr = userCmd.Wait()
	if waitErr != nil {
		return -1, fmt.Errorf("user shell wait: %w", waitErr)
	}
	debuglog.Logf("shell", "user shell exited rc=%d; leaving anchor pid=%d alive", rc, anchorPid)
	return rc, nil
}

// formatAnchorOutput returns the captured anchor stdout/stderr formatted
// for inclusion in an error message. Returns "" when nothing was captured
// (so the error message stays clean in the normal case).
func formatAnchorOutput(buf *lineRingBuffer) string {
	if buf == nil || buf.IsEmpty() {
		return ""
	}
	return "\n--- anchor output ---\n" + strings.TrimRight(buf.String(), "\n") + "\n---"
}

func applyDefaults(d *ShellDeps) {
	if d.WaitForRunning == 0 {
		d.WaitForRunning = 60 * time.Second
	}
	if d.PollInterval == 0 {
		d.PollInterval = 250 * time.Millisecond
	}
}

// runUserShell is the already-running shortcut: just attach to the
// existing sandbox. We deliberately skip port reconcile here because
// the sandbox came up via an earlier devm shell that already ran
// reconcile; rerunning would be cheap but adds a noticeable startup
// delay for the common case. If devm.yaml ports have changed since
// the last cold start, the user must `devm stop` and re-shell.
func runUserShell(d ShellDeps, cfg schema.Config, repoRoot, sandboxName, cmdName string, cmdArgs []string) (int, error) {
	wrapper := filepath.Join(repoRoot, ".devm", "scripts", "with-devm-env")
	args := []string{"exec", "-it"}
	args = append(args, sandbox.EnvArgs(cfg)...)
	args = append(args, sandboxName, wrapper, cmdName)
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
