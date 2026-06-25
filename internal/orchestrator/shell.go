package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/creack/pty"
	"github.com/mdubb86/devm/internal/debuglog"
	"github.com/mdubb86/devm/internal/lock"
	"github.com/mdubb86/devm/internal/render"
	"github.com/mdubb86/devm/internal/sandbox"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/status"
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

	reporter := status.New(os.Stderr)
	defer reporter.Stop()
	reporter.Start("starting up")

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
		reporter.Step("attaching to running sandbox", false)
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
		reporter.Step("ready", false)
		reporter.Stop()
		reporter.Clear()
		return runUserShell(d, cfg, repoRoot, sandboxName, cmdName, cmdArgs)
	}

	// Cold start.
	// Compute total user steps and begin the cold-start reporter sequence.
	userTotal := len(cfg.Install)
	for _, svc := range cfg.Services {
		userTotal += len(svc.Startup)
	}
	reporter.SetTotal(userTotal)
	reporter.Step("spawning sandbox", false)

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
	// freshlyCreated tracks whether this RunShell is creating the sandbox
	// (vs restarting an existing one). On abort/error before handoff we
	// only `sbx rm -f` when we created it — restarting a user's existing
	// sandbox and failing mid-install must not destroy their state.
	freshlyCreated := !sb.Exists()
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
		anchorDead = make(chan struct{}) // closed when anchor exits; passed to gate pollers
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
			close(anchorDead)
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
			close(anchorDead)
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
	// diagnostic gaps (cold-start "no sandbox named <name>" failures
	// gave us zero clue what install: was up to).
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
		// Tear down a freshly-created, half-built sandbox. SIGKILL on
		// the anchor doesn't give sbx a chance to clean up its sandbox
		// state, so without an explicit `sbx rm -f` the user is left
		// with a broken sandbox that the next `devm shell` will try to
		// restart (and re-fail). Only do this for sandboxes WE created
		// in this RunShell — restarting an existing sandbox and failing
		// mid-install must not destroy the user's data.
		if freshlyCreated {
			debuglog.Logf("shell", "cleanup: sbx rm -f %s (freshly-created, abort)", sandboxName)
			_, _ = d.Runner.Output("sbx", "rm", "-f", sandboxName)
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
				reporter.Fail()
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

	// Network policies BEFORE the install gate. Install steps routinely
	// curl from external mirrors (deb.nodesource.com, dl.cloudsmith.io,
	// claude.ai/install.sh — anything in cfg.Network.AllowedDomains).
	// Sbx default-denies; without the allow rules in place the curls
	// 403 and install: aborts before the sentinel can appear.
	debuglog.Logf("shell", "network-reconcile: starting")
	if err := ReconcileNetworkWithRunner(sb, cfg, d.Runner); err != nil {
		return -1, fmt.Errorf("network reconcile failed: %w", err)
	}
	debuglog.Logf("shell", "network-reconcile: done")

	// Install gate: poll for /tmp/.devm-install/install-all-ok. Closes
	// the async-runtime-death race (the 2026-06-05 bootstrap.sh revert).
	// Sbx reports status=running before install: finishes; the sentinel
	// only appears AFTER all install steps complete.
	// On anchor death (sbx tears sandbox down per c02), switch to the
	// host-side failure mirror written by wrap-fg.sh (pinned by c32-c34).
	installTimeout := gateTimeoutFromEnv("install", defaultInstallGateTimeout)
	if err := waitForPhaseSentinel(ctx, sb, "install", anchorDead, installTimeout, defaultGatePollInterval, reporter, cfg); err != nil {
		reporter.Fail()
		var report *FailureReport
		if errors.Is(err, ErrAnchorDied) {
			report, _ = readPhaseFailureFromHost(repoRoot, "install", cfg)
		} else {
			report, _ = readPhaseFailure(sb, "install", cfg)
		}
		if report == nil {
			return -1, fmt.Errorf("%w", err)
		}
		return -1, fmt.Errorf("%s", formatFailureReport(report))
	}

	// Port reconcile and snapshot BEFORE user shell. The anchor is
	// alive, holding the session — publishes stick immediately and
	// without the phantom/vanish dance the old flow had to defend
	// against (Quirk #2, #3 in docs/sbx-quirks.md). Pinned by
	// e2e/test_sbx_anchor_05_publish_sticks.py and
	// e2e/test_sbx_anchor_09_port_cycle.py.
	reporter.Step("reconciling ports", false)
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

	// Startup gate: poll for /tmp/.devm-startup/startup-all-ok. Closes
	// the silent-startup-failure gap (contract_24). Per contract_29, sbx
	// halts at the first failing startup step but doesn't surface it
	// — the sentinel is the only signal devm has.
	// On anchor death, switch to the host-side failure mirror (c32-c34).
	startupTimeout := gateTimeoutFromEnv("startup", defaultStartupGateTimeout)
	if err := waitForPhaseSentinel(ctx, sb, "startup", anchorDead, startupTimeout, defaultGatePollInterval, reporter, cfg); err != nil {
		reporter.Fail()
		var report *FailureReport
		if errors.Is(err, ErrAnchorDied) {
			report, _ = readPhaseFailureFromHost(repoRoot, "startup", cfg)
		} else {
			report, _ = readPhaseFailure(sb, "startup", cfg)
		}
		if report == nil {
			return -1, fmt.Errorf("%w", err)
		}
		return -1, fmt.Errorf("%s", formatFailureReport(report))
	}

	// Signal success and clear the terminal before handing off to the
	// user's interactive shell. Stop+Clear MUST happen here — the user's
	// shell takes over stderr after this point.
	reporter.Step("ready", false)
	reporter.Stop()
	reporter.Clear()

	// Spawn the user's interactive shell. UserSpawner inherits the host
	// terminal's stdin/stdout/stderr, so the user shell ends up in the
	// same session as devm (which is in the same session as the
	// anchor — Go exec.Cmd default). Same-session is required for
	// daemon survival under sbx (see docs/sbx-quirks.md section 5).
	execArgs := buildShellExecArgs(cfg, repoRoot, sandboxName, cmdName, cmdArgs)
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
// buildShellExecArgs is the single source of truth for the `sbx exec`
// argv used by both cold-start (RunShell) and warm reattach
// (runUserShell). Anything added at the sbx-exec layer — flags, env
// forwarding, terminfo forwarding — must go here so cold and warm
// shells stay shape-identical. The shape:
//
//	sbx exec -it -w <repoRoot> <EnvArgs> [-e DEVM_TERMINFO_BLOB=...] \
//	    <sandboxName> <wrapper> <cmdName> <cmdArgs...>
//
//	-w repoRoot   lands the user in $WORKSPACE on shell entry
//	              (sbx's mirrored-path mount maps host == sandbox)
//	EnvArgs       per-session host-forwarded terminal vars
//	              (TERM/COLORTERM/LANG/LC_ALL/LC_CTYPE)
//	DEVM_TERMINFO_BLOB
//	              base64'd host infocmp output, decoded + installed
//	              by the with-devm-env wrapper when missing in
//	              the sandbox's terminfo db. Empty → no -e flag.
//	wrapper       .devm/scripts/with-devm-env sources .devm/.env
//	              and may install the forwarded terminfo entry.
//
// Cold/warm parity is pinned by TestBuildShellExecArgs_Shape.
func buildShellExecArgs(cfg schema.Config, repoRoot, sandboxName, cmdName string, cmdArgs []string) []string {
	wrapper := filepath.Join(repoRoot, ".devm", "scripts", "with-devm-env")
	args := []string{"exec", "-it", "-w", repoRoot}
	args = append(args, sandbox.EnvArgs(cfg)...)
	if blob := captureHostTerminfo(); blob != "" {
		args = append(args, "-e", "DEVM_TERMINFO_BLOB="+blob)
	}
	args = append(args, sandboxName, wrapper, cmdName)
	return append(args, cmdArgs...)
}

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
	args := buildShellExecArgs(cfg, repoRoot, sandboxName, cmdName, cmdArgs)
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
