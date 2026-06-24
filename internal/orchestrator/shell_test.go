package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mdubb86/devm/internal/sandbox"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/status"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// stubSpawner records Start calls and hands out scripted SpawnedCmds
// in FIFO order. If the queue is empty, returns a fresh stubCmd whose
// Wait will block forever.
type stubSpawner struct {
	mu       sync.Mutex
	started  [][]string
	cmds     []*stubCmd
	cmdQueue []*stubCmd
}

func (s *stubSpawner) Start(name string, args ...string) (SpawnedCmd, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.started = append(s.started, append([]string{name}, args...))
	if len(s.cmdQueue) > 0 {
		c := s.cmdQueue[0]
		s.cmdQueue = s.cmdQueue[1:]
		s.cmds = append(s.cmds, c)
		return c, nil
	}
	c := &stubCmd{waitErr: make(chan error, 1)}
	s.cmds = append(s.cmds, c)
	return c, nil
}

type stubCmd struct {
	waitErr chan error
	exitRC  int // exit code returned alongside waitErr
	killed  bool
	pid     int
}

func (c *stubCmd) Wait() (int, error) {
	err := <-c.waitErr
	return c.exitRC, err
}
func (c *stubCmd) Kill() error {
	c.killed = true
	c.exitRC = -1
	// Non-blocking send so Kill is idempotent / safe to call multiple times.
	select {
	case c.waitErr <- errors.New("killed"):
	default:
	}
	return nil
}
func (c *stubCmd) Pid() int { return c.pid }

// stateRunner: scripted responses for sbx ls (running/stopped),
// sbx ports --json, and the Sessions probe script. Concurrency-safe.
type stateRunner struct {
	mu       sync.Mutex
	calls    [][]string
	lsStatus string
	lsAbsent bool
	probeOut string
	portsOut string
	catOut   string // stdout returned for `sbx exec <name> cat <path>` (snapshot reads)
}

func (r *stateRunner) Output(name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	all := append([]string{name}, args...)
	r.calls = append(r.calls, all)
	joined := strings.Join(all, " ")
	switch {
	case strings.Contains(joined, "sbx ls"):
		// State() expects columns: NAME IMAGE STATUS ...
		if r.lsAbsent {
			return []byte("SANDBOX  IMAGE  STATUS\n"), nil
		}
		return []byte("SANDBOX  IMAGE  STATUS\nx-sbx    img    " + r.lsStatus + "\n"), nil
	case strings.Contains(joined, "ports") && strings.Contains(joined, "--json"):
		if r.portsOut != "" {
			return []byte(r.portsOut), nil
		}
		return []byte("[]"), nil
	case strings.Contains(joined, "sh") && strings.Contains(joined, "-c"):
		// Sessions probe script (and also matches WriteSnapshot's
		// `sh -c "... cat > ..."` invocation, which is fine — we don't
		// care about that case's return value in these tests).
		return []byte(r.probeOut), nil
	case strings.Contains(joined, "exec") && strings.Contains(joined, "cat "):
		// Snapshot read: `sbx exec <name> cat /home/agent/.devm/applied.yaml`.
		return []byte(r.catOut), nil
	}
	return []byte(""), nil
}
func (r *stateRunner) Run(name string, args ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, append([]string{name}, args...))
	return nil
}
func (r *stateRunner) RunStdin(stdin, name string, args ...string) error {
	return r.Run(name, args...)
}

func minimalCfg() schema.Config {
	return schema.Config{
		Project:  schema.Project{ID: "x", SandboxName: "x-sbx", PortOffset: 60000},
		Services: map[string]schema.Service{},
	}
}

func TestRunShellAlreadyRunningTakesShortcut(t *testing.T) {
	repoRoot := t.TempDir()
	spawner := &stubSpawner{}
	runner := &stateRunner{lsStatus: "running"}

	// User shell exits immediately with success.
	userCmd := &stubCmd{waitErr: make(chan error, 1)}
	userCmd.waitErr <- nil
	spawner.cmdQueue = []*stubCmd{userCmd}

	deps := ShellDeps{
		AnchorSpawner: spawner,
		UserSpawner:   spawner,
		Runner:        runner,
		LockPath:      filepath.Join(repoRoot, ".devm/lock"),
	}
	rc, err := RunShell(context.Background(), deps, minimalCfg(), repoRoot, "x-sbx", "bash", nil)
	require.NoError(t, err)
	assert.Equal(t, 0, rc)

	// Only ONE Start should have been called (the user shell), not two.
	require.Len(t, spawner.started, 1)
	assert.Contains(t, strings.Join(spawner.started[0], " "), "sbx exec")
	assert.Contains(t, strings.Join(spawner.started[0], " "), "x-sbx")

	// No port reconcile on shortcut path.
	for _, c := range runner.calls {
		joined := strings.Join(c, " ")
		require.False(t, strings.Contains(joined, "--publish"), "should not reconcile on shortcut: %s", joined)
		require.False(t, strings.Contains(joined, "--unpublish"), "should not reconcile on shortcut: %s", joined)
	}
}

func TestRunShellInjectsServiceEnv(t *testing.T) {
	repoRoot := t.TempDir()
	spawner := &stubSpawner{}
	runner := &stateRunner{lsStatus: "running"}

	userCmd := &stubCmd{waitErr: make(chan error, 1)}
	userCmd.waitErr <- nil
	spawner.cmdQueue = []*stubCmd{userCmd}

	cfg := minimalCfg()
	cfg.Services = map[string]schema.Service{
		"api": {Port: 8080, Env: map[string]string{"LOG_LEVEL": "info"}},
	}

	deps := ShellDeps{
		AnchorSpawner: spawner,
		UserSpawner:   spawner,
		Runner:        runner,
		LockPath:      filepath.Join(repoRoot, ".devm/lock"),
	}
	rc, err := RunShell(context.Background(), deps, cfg, repoRoot, "x-sbx", "bash", nil)
	require.NoError(t, err)
	assert.Equal(t, 0, rc)

	require.Len(t, spawner.started, 1)
	joined := strings.Join(spawner.started[0], " ")

	// New contract: persistent project + service env lives in /.devm/.env,
	// sourced by the with-devm-env wrapper. Verify the wrapper is in argv
	// AND the rendered .env contains the flattened service env.
	assert.Contains(t, joined, filepath.Join(repoRoot, ".devm", "scripts", "with-devm-env"),
		"sbx exec must invoke via the with-devm-env wrapper")
	assert.NotContains(t, joined, "API_LOG_LEVEL=info",
		"service env must NOT ride on -e flags anymore; it lives in .devm/.env")

	bs, err := os.ReadFile(filepath.Join(repoRoot, ".devm", ".env"))
	require.NoError(t, err)
	assert.Contains(t, string(bs), `export API_LOG_LEVEL='info'`,
		"service env must be flattened into .devm/.env (NAME_KEY)")
}

func TestRunShellColdStartHappyPath(t *testing.T) {
	repoRoot := t.TempDir()

	runner := &stateRunner{
		lsAbsent: true,                    // sandbox doesn't exist initially
		probeOut: "27 bash pts/1 agent\n", // pty appears for the user shell
	}

	// runCmd: the sbx run subprocess; blocks on Wait until Killed.
	runCmd := &stubCmd{waitErr: make(chan error, 1)}
	// userCmd: the user shell; exits immediately.
	userCmd := &stubCmd{waitErr: make(chan error, 1)}
	userCmd.waitErr <- nil

	spawner := &stubSpawner{cmdQueue: []*stubCmd{runCmd, userCmd}}

	// Flip state to "running" shortly after the orchestrator calls sbx ls.
	go func() {
		time.Sleep(40 * time.Millisecond)
		runner.mu.Lock()
		runner.lsAbsent = false
		runner.lsStatus = "running"
		runner.mu.Unlock()
	}()

	deps := ShellDeps{
		AnchorSpawner:  spawner,
		UserSpawner:    spawner,
		Runner:         runner,
		LockPath:       filepath.Join(repoRoot, ".devm/lock"),
		WaitForRunning: 2 * time.Second,
		PollInterval:   20 * time.Millisecond,
	}
	rc, err := RunShell(context.Background(), deps, minimalCfg(), repoRoot, "x-sbx", "bash", nil)
	require.NoError(t, err)
	assert.Equal(t, 0, rc)

	require.GreaterOrEqual(t, len(spawner.started), 2)
	// Anchor is spawned as bare `sbx run` (no nohup wrap). sbx 0.31
	// ignores SIGHUP under a controlling TTY, so the historical
	// nohup wrap is redundant. Pinned by
	// e2e/test_sbx_interop_02_anchor_master_close_lifetime.py.
	assert.Equal(t, "sbx", spawner.started[0][0],
		"anchor argv[0] must be `sbx`")
	assert.Equal(t, "run", spawner.started[0][1],
		"argv[1] must be `run`")
	assert.Contains(t, strings.Join(spawner.started[1], " "), "sbx exec",
		"user shell stays plain `sbx exec ...`")

	// Verify --name flag is passed with the expected sandbox name.
	runArgsJoined := strings.Join(spawner.started[0], " ")
	assert.Contains(t, runArgsJoined, "--name x-sbx",
		"sbx run must use --name so the actual sandbox name matches what we look up later")
	assert.Contains(t, runArgsJoined, " x ", // agent positional (project.ID from minimalCfg)
		"agent positional must be cfg.Project.ID")

	// New architecture: anchor stays alive. runCmd must NOT be killed
	// on the normal cold-start path. The anchor exits on its own when
	// `sbx stop NAME` runs later (pinned by
	// e2e/test_sbx_anchor_04_sbx_stop_reaps_anchor.py).
	assert.False(t, runCmd.killed,
		"anchor must NOT be killed during cold-start; it lives until sbx stop")

	// Ordering invariant: anchor is spawned before the user shell so
	// the sandbox is up and exec-ready before any sbx exec attaches.
	require.GreaterOrEqual(t, len(spawner.started), 2,
		"both anchor (sbx run) and user shell (sbx exec) must be spawned")
}

func TestRunShellRestartUsesKitName(t *testing.T) {
	repoRoot := t.TempDir()

	// Sandbox already exists in stopped state (lsAbsent=false, lsStatus="stopped").
	runner := &stateRunner{
		lsStatus: "stopped",
		probeOut: "27 bash pts/1 agent\n",
	}

	runCmd := &stubCmd{waitErr: make(chan error, 1)}
	userCmd := &stubCmd{waitErr: make(chan error, 1)}
	userCmd.waitErr <- nil

	spawner := &stubSpawner{cmdQueue: []*stubCmd{runCmd, userCmd}}

	go func() {
		time.Sleep(40 * time.Millisecond)
		runner.mu.Lock()
		runner.lsStatus = "running"
		runner.mu.Unlock()
	}()

	deps := ShellDeps{
		AnchorSpawner:  spawner,
		UserSpawner:    spawner,
		Runner:         runner,
		LockPath:       filepath.Join(repoRoot, ".devm/lock"),
		WaitForRunning: 2 * time.Second,
		PollInterval:   20 * time.Millisecond,
	}
	rc, err := RunShell(context.Background(), deps, minimalCfg(), repoRoot, "x-sbx", "bash", nil)
	require.NoError(t, err)
	assert.Equal(t, 0, rc)

	// Restart path: `sbx run --kit <dir> <sandbox-name>` — kit provides
	// the agent definition (sbx doesn't remember across restarts); no
	// --name (sbx rejects it for existing sandboxes).
	runArgsJoined := strings.Join(spawner.started[0], " ")
	assert.Contains(t, runArgsJoined, "sbx run --kit",
		"restart path must pass --kit so sbx can resolve the custom agent")
	assert.Contains(t, runArgsJoined, "x-sbx",
		"restart path must include the sandbox name as positional")
	assert.NotContains(t, runArgsJoined, "--name",
		"restart path must NOT pass --name (sbx rejects it for existing sandboxes)")
	// Also: agent positional and workspace positional are NOT passed on
	// restart — sbx infers them from the sandbox name + loaded kit.
	// We can't easily assert their absence without false positives, but
	// asserting the exact arg count is a clean substitute:
	expectedArgs := []string{"sbx", "run", "--kit", filepath.Join(repoRoot, ".devm"), "x-sbx"}
	assert.Equal(t, expectedArgs, spawner.started[0],
		"restart path argv should be exactly: sbx run --kit <kitdir> <sandbox-name>")
}

func TestRunShellWaitForRunningTimesOut(t *testing.T) {
	repoRoot := t.TempDir()
	runner := &stateRunner{lsStatus: "stopped"} // never flips
	runCmd := &stubCmd{waitErr: make(chan error, 1)}
	spawner := &stubSpawner{cmdQueue: []*stubCmd{runCmd}}

	deps := ShellDeps{
		AnchorSpawner:  spawner,
		UserSpawner:    spawner,
		Runner:         runner,
		LockPath:       filepath.Join(repoRoot, ".devm/lock"),
		WaitForRunning: 100 * time.Millisecond,
		PollInterval:   20 * time.Millisecond,
	}
	_, err := RunShell(context.Background(), deps, minimalCfg(), repoRoot, "x-sbx", "bash", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "never reached running")
	assert.True(t, runCmd.killed, "run subprocess must be killed on failure")
}

func TestRunShellAbort_FreshlyCreated_RunsSbxRm(t *testing.T) {
	// Cold-start path: sandbox doesn't exist initially, anchor spawned,
	// but sandbox never reaches running (timeout). Cleanup defer MUST run
	// `sbx rm -f <name>` so the half-built sandbox doesn't linger.
	repoRoot := t.TempDir()
	runner := &stateRunner{lsAbsent: true} // sandbox doesn't exist → freshlyCreated=true
	runCmd := &stubCmd{waitErr: make(chan error, 1)}
	spawner := &stubSpawner{cmdQueue: []*stubCmd{runCmd}}

	deps := ShellDeps{
		AnchorSpawner:  spawner,
		UserSpawner:    spawner,
		Runner:         runner,
		LockPath:       filepath.Join(repoRoot, ".devm/lock"),
		WaitForRunning: 100 * time.Millisecond,
		PollInterval:   20 * time.Millisecond,
	}
	_, err := RunShell(context.Background(), deps, minimalCfg(), repoRoot, "x-sbx", "bash", nil)
	require.Error(t, err)

	sawRm := false
	runner.mu.Lock()
	for _, c := range runner.calls {
		if strings.Join(c, " ") == "sbx rm -f x-sbx" {
			sawRm = true
			break
		}
	}
	runner.mu.Unlock()
	assert.True(t, sawRm, "cold-start abort must `sbx rm -f` the half-built sandbox; got calls: %v", runner.calls)
}

func TestRunShellAbort_Restart_DoesNotRunSbxRm(t *testing.T) {
	// Restart path: sandbox EXISTS initially but is stopped. RunShell will
	// restart it. If install/startup fails, we must NOT `sbx rm -f` —
	// that would destroy the user's existing sandbox data.
	repoRoot := t.TempDir()
	runner := &stateRunner{lsStatus: "stopped"} // exists, but not running; stays stopped
	runCmd := &stubCmd{waitErr: make(chan error, 1)}
	spawner := &stubSpawner{cmdQueue: []*stubCmd{runCmd}}

	deps := ShellDeps{
		AnchorSpawner:  spawner,
		UserSpawner:    spawner,
		Runner:         runner,
		LockPath:       filepath.Join(repoRoot, ".devm/lock"),
		WaitForRunning: 100 * time.Millisecond,
		PollInterval:   20 * time.Millisecond,
	}
	_, err := RunShell(context.Background(), deps, minimalCfg(), repoRoot, "x-sbx", "bash", nil)
	require.Error(t, err)

	runner.mu.Lock()
	for _, c := range runner.calls {
		joined := strings.Join(c, " ")
		assert.NotEqual(t, "sbx rm -f x-sbx", joined,
			"restart-path abort must NOT destroy an existing sandbox")
	}
	runner.mu.Unlock()
}

// Compile-time guards (so accidental refactors that delete a symbol
// fail at build time rather than at runtime). Unused locals are fine.
var _ = sandbox.DefaultRunner{}

func TestRunShellColdStartWritesSnapshot(t *testing.T) {
	repoRoot := t.TempDir()

	runner := &stateRunner{
		lsAbsent: true,
		probeOut: "27 bash pts/1 agent\n",
	}
	runCmd := &stubCmd{waitErr: make(chan error, 1)}
	userCmd := &stubCmd{waitErr: make(chan error, 1)}
	userCmd.waitErr <- nil

	spawner := &stubSpawner{cmdQueue: []*stubCmd{runCmd, userCmd}}

	go func() {
		time.Sleep(40 * time.Millisecond)
		runner.mu.Lock()
		runner.lsAbsent = false
		runner.lsStatus = "running"
		runner.mu.Unlock()
	}()

	deps := ShellDeps{
		AnchorSpawner:  spawner,
		UserSpawner:    spawner,
		Runner:         runner,
		LockPath:       filepath.Join(repoRoot, ".devm/lock"),
		WaitForRunning: 2 * time.Second,
		PollInterval:   20 * time.Millisecond,
	}
	rc, err := RunShell(context.Background(), deps, minimalCfg(), repoRoot, "x-sbx", "bash", nil)
	require.NoError(t, err)
	assert.Equal(t, 0, rc)

	// Verify the snapshot was written: look for any runner call whose
	// args contain "applied.yaml" (the snapshot path or the .tmp form).
	sawSnapshotWrite := false
	runner.mu.Lock()
	for _, c := range runner.calls {
		joined := strings.Join(c, " ")
		if strings.Contains(joined, "applied.yaml") {
			sawSnapshotWrite = true
			break
		}
	}
	runner.mu.Unlock()
	assert.True(t, sawSnapshotWrite, "cold-start must write snapshot via sbx exec ... applied.yaml")
}

func TestRunShellShortcutInvokesReconcileInner(t *testing.T) {
	// Shortcut path with a snapshot that matches cfg → reconcile inner
	// finds no diff → no recreate surface, no LIVE applies → user shell
	// attaches normally.
	repoRoot := t.TempDir()
	cfg := minimalCfg()
	snapYAML, _ := yaml.Marshal(cfg)
	runner := &stateRunner{
		lsStatus: "running",
		catOut:   string(snapYAML),
	}

	userCmd := &stubCmd{waitErr: make(chan error, 1)}
	userCmd.waitErr <- nil
	spawner := &stubSpawner{cmdQueue: []*stubCmd{userCmd}}

	deps := ShellDeps{
		AnchorSpawner: spawner,
		UserSpawner:   spawner,
		Runner:        runner,
		LockPath:      filepath.Join(repoRoot, ".devm/lock"),
	}
	rc, err := RunShell(context.Background(), deps, cfg, repoRoot, "x-sbx", "bash", nil)
	require.NoError(t, err)
	assert.Equal(t, 0, rc)

	// Reconcile-inner ran (visible via the snapshot read call).
	sawCat := false
	runner.mu.Lock()
	for _, c := range runner.calls {
		joined := strings.Join(c, " ")
		if strings.Contains(joined, "cat ") && strings.Contains(joined, "applied.yaml") {
			sawCat = true
			break
		}
	}
	runner.mu.Unlock()
	assert.True(t, sawCat, "shortcut path must invoke RunReconcileInner which reads the snapshot")
}

// stubRunnerForFailureReader returns scripted outputs for the sbx exec
// calls readPhaseFailure makes. The map key is a substring of the joined
// command string; value is (stdout, error). First substring match wins.
type stubRunnerForFailureReader struct {
	t        *testing.T
	mu       sync.Mutex
	scripted map[string]struct {
		out []byte
		err error
	}
	calls [][]string
}

func (r *stubRunnerForFailureReader) Output(name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, append([]string{name}, args...))
	full := strings.Join(append([]string{name}, args...), " ")
	for prefix, scripted := range r.scripted {
		if strings.Contains(full, prefix) {
			return scripted.out, scripted.err
		}
	}
	return nil, fmt.Errorf("stubRunnerForFailureReader: no scripted response for %q", full)
}

func (r *stubRunnerForFailureReader) Run(name string, args ...string) error {
	_, err := r.Output(name, args...)
	return err
}

func (r *stubRunnerForFailureReader) RunStdin(stdin, name string, args ...string) error {
	return r.Run(name, args...)
}

func TestReadPhaseFailure_IdentifiesFirstFailingFGStep(t *testing.T) {
	// Render index: bootstrap(1), cfg.Install[0]=step 2,
	// cfg.Install[1]=step 3. In this scenario bootstrap + step 2 succeed;
	// step 3 (cfg.Install[1] = "apt-get install -y foo") fails with rc=7.
	r := &stubRunnerForFailureReader{
		t: t,
		scripted: map[string]struct {
			out []byte
			err error
		}{
			"ls /tmp/.devm-install/": {out: []byte(
				"install-1.ok\ninstall-1.rc\n" +
					"install-2.ok\ninstall-2.rc\n" +
					"install-3.rc\n"), err: nil},
			"cat /tmp/.devm-install/install-1.rc": {out: []byte("0\n"), err: nil},
			"cat /tmp/.devm-install/install-2.rc": {out: []byte("0\n"), err: nil},
			"cat /tmp/.devm-install/install-3.rc": {out: []byte("7\n"), err: nil},
			"cat /tmp/.devm-install/install-3/current": {out: []byte(
				"@4000000067432a1b0d2f8e4c Reading package lists...\n" +
					"@4000000067432a1c12a4b7d3 E: Unable to locate package foo\n"), err: nil},
		},
	}
	sb := &sandbox.Sandbox{Name: "x", Runner: r}
	cfg := schema.Config{Install: []string{"apt-get update", "apt-get install -y foo"}}

	report, err := readPhaseFailure(sb, "install", cfg)
	require.NoError(t, err)
	require.NotNil(t, report)
	assert.Equal(t, "install", report.Phase)
	assert.Equal(t, 3, report.StepN)
	assert.Equal(t, 7, report.RC)
	assert.False(t, report.Hung)
	assert.Equal(t, "apt-get install -y foo", report.UserCmd)
	assert.Contains(t, report.CapturedTail, "Unable to locate package foo")
}

func TestReadPhaseFailure_HungStep(t *testing.T) {
	// bootstrap + step 2 succeed; step 3 hung (no .rc, no .ok).
	r := &stubRunnerForFailureReader{
		t: t,
		scripted: map[string]struct {
			out []byte
			err error
		}{
			"ls /tmp/.devm-install/": {out: []byte(
				"install-1.ok\ninstall-1.rc\n" +
					"install-2.ok\ninstall-2.rc\n"), err: nil},
			"cat /tmp/.devm-install/install-1.rc": {out: []byte("0\n"), err: nil},
			"cat /tmp/.devm-install/install-2.rc": {out: []byte("0\n"), err: nil},
			"cat /tmp/.devm-install/install-3/current": {out: []byte("partial output\n"), err: nil},
		},
	}
	sb := &sandbox.Sandbox{Name: "x", Runner: r}
	cfg := schema.Config{Install: []string{"apt-get update", "sleep 200"}}

	report, err := readPhaseFailure(sb, "install", cfg)
	require.NoError(t, err)
	require.NotNil(t, report)
	assert.Equal(t, 3, report.StepN)
	assert.True(t, report.Hung)
	assert.Equal(t, -1, report.RC)
	assert.Equal(t, "sleep 200", report.UserCmd)
	assert.Contains(t, report.CapturedTail, "partial output")
}

func TestFormatFailureReport_StepFailure(t *testing.T) {
	r := &FailureReport{
		Phase:        "install",
		StepN:        3,
		RC:           1,
		UserCmd:      "apt-get install -y nonexistent-pkg",
		CapturedTail: "E: Unable to locate package nonexistent-pkg\n",
	}
	out := formatFailureReport(r)
	assert.Contains(t, out, "error: install step 3 failed (rc=1)")
	assert.Contains(t, out, "command: apt-get install -y nonexistent-pkg")
	assert.Contains(t, out, "/tmp/.devm-install/install-3/current")
	assert.Contains(t, out, "Unable to locate package nonexistent-pkg")
}

func TestFormatFailureReport_HungStep(t *testing.T) {
	r := &FailureReport{
		Phase:        "install",
		StepN:        2,
		RC:           -1,
		Hung:         true,
		UserCmd:      "apt-get install -y mongodb-org",
		CapturedTail: "partial output\n",
	}
	out := formatFailureReport(r)
	assert.Contains(t, out, "still running or hung")
	assert.Contains(t, out, "step 2")
	assert.Contains(t, out, "apt-get install -y mongodb-org")
	assert.Contains(t, out, "partial output")
}

func TestFormatFailureReport_TruncationNoted(t *testing.T) {
	r := &FailureReport{
		Phase:        "install",
		StepN:        2,
		RC:           1,
		UserCmd:      "bigcmd",
		CapturedTail: strings.Repeat("x", 100),
		Truncated:    true,
	}
	out := formatFailureReport(r)
	assert.Contains(t, out, "truncated")
}

func TestWaitForPhaseSentinel_SentinelPresent(t *testing.T) {
	r := &stubRunnerForFailureReader{
		t: t,
		scripted: map[string]struct {
			out []byte
			err error
		}{
			"test -f /tmp/.devm-install/install-all-ok": {out: []byte(""), err: nil},
			"ls /tmp/.devm-install/":                    {out: []byte(""), err: nil},
		},
	}
	sb := &sandbox.Sandbox{Name: "x", Runner: r}
	err := waitForPhaseSentinel(context.Background(), sb, "install", nil, 5*time.Second, 50*time.Millisecond, &status.NoOpReporter{}, schema.Config{})
	assert.NoError(t, err)
}

func TestWaitForPhaseSentinel_TimesOut(t *testing.T) {
	r := &stubRunnerForFailureReader{
		t: t,
		scripted: map[string]struct {
			out []byte
			err error
		}{
			"test -f /tmp/.devm-install/install-all-ok": {out: []byte(""), err: errors.New("exit 1")},
			"ls /tmp/.devm-install/":                    {out: []byte(""), err: nil},
		},
	}
	sb := &sandbox.Sandbox{Name: "x", Runner: r}
	start := time.Now()
	err := waitForPhaseSentinel(context.Background(), sb, "install", nil, 300*time.Millisecond, 50*time.Millisecond, &status.NoOpReporter{}, schema.Config{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "did not complete")
	assert.GreaterOrEqual(t, time.Since(start), 300*time.Millisecond,
		"must respect timeout")
}

func TestWaitForPhaseSentinel_AnchorDied(t *testing.T) {
	r := &stubRunnerForFailureReader{
		t: t,
		scripted: map[string]struct {
			out []byte
			err error
		}{
			"test -f /tmp/.devm-install/install-all-ok": {out: []byte(""), err: errors.New("exit 1")},
			"ls /tmp/.devm-install/":                    {out: []byte(""), err: nil},
		},
	}
	sb := &sandbox.Sandbox{Name: "x", Runner: r}
	runDone := make(chan struct{})
	close(runDone) // anchor already dead

	err := waitForPhaseSentinel(context.Background(), sb, "install", runDone, 5*time.Second, 50*time.Millisecond, &status.NoOpReporter{}, schema.Config{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrAnchorDied), "must report anchor death, not timeout")
}

func TestWaitForPhaseSentinel_CtxCanceled(t *testing.T) {
	// SIGINT during install phase cancels ctx. Gate must abandon polling
	// promptly with ctx.Err() so the surrounding RunShell defer can kill
	// the anchor and tear down the half-built sandbox.
	r := &stubRunnerForFailureReader{
		t: t,
		scripted: map[string]struct {
			out []byte
			err error
		}{
			"test -f /tmp/.devm-install/install-all-ok": {out: []byte(""), err: errors.New("exit 1")},
			"ls /tmp/.devm-install/":                    {out: []byte(""), err: nil},
		},
	}
	sb := &sandbox.Sandbox{Name: "x", Runner: r}
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after a short delay so the gate is mid-poll.
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	err := waitForPhaseSentinel(ctx, sb, "install", nil, 10*time.Second, 50*time.Millisecond, &status.NoOpReporter{}, schema.Config{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled), "must surface ctx cancel, got %v", err)
	assert.Less(t, time.Since(start), 1*time.Second, "must abort promptly on ctx cancel, not wait for timeout")
}

func TestReadPhaseFailureFromHost_FindsLowestN(t *testing.T) {
	dir := t.TempDir()
	failuresDir := filepath.Join(dir, ".devm", "failures")
	require.NoError(t, os.MkdirAll(failuresDir, 0o755))

	// step 3 failed (rc=7); also leave a stray step 5 to confirm "lowest N wins"
	require.NoError(t, os.WriteFile(filepath.Join(failuresDir, "install-3.rc"), []byte("7\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(failuresDir, "install-3.current"), []byte("E: bad package\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(failuresDir, "install-5.rc"), []byte("1\n"), 0o644))

	cfg := schema.Config{Install: []string{"apt-get update", "apt-get install -y foo", "apt-get install -y bar"}}
	report, err := readPhaseFailureFromHost(dir, "install", cfg)
	require.NoError(t, err)
	require.NotNil(t, report)
	assert.Equal(t, 3, report.StepN, "must return lowest N")
	assert.Equal(t, 7, report.RC)
	assert.Contains(t, report.CapturedTail, "E: bad package")
	// Step 3 = cfg.Install[1] per the render index (bootstrap=1, user[0]=2, user[1]=3).
	assert.Equal(t, "apt-get install -y foo", report.UserCmd)
}

func TestReadPhaseFailureFromHost_NoFailuresDir(t *testing.T) {
	dir := t.TempDir()
	report, err := readPhaseFailureFromHost(dir, "install", schema.Config{})
	require.NoError(t, err)
	assert.Nil(t, report)
}

// recordingReporter captures reporter calls for assertions in tests that
// want to verify the step announcement behavior of waitForPhaseSentinel.
type recordingReporter struct {
	steps []struct {
		desc    string
		counted bool
	}
	stops  int
	clears int
	failed bool
}

func (r *recordingReporter) Start(string)              {}
func (r *recordingReporter) SetTotal(int)              {}
func (r *recordingReporter) Step(desc string, counted bool) {
	r.steps = append(r.steps, struct {
		desc    string
		counted bool
	}{desc, counted})
}
func (r *recordingReporter) Fail()       { r.failed = true }
func (r *recordingReporter) Info(string) {}
func (r *recordingReporter) Stop()       { r.stops++ }
func (r *recordingReporter) Clear()      { r.clears++ }

func TestWaitForPhaseSentinel_SentinelPresent_AnnouncesStep1(t *testing.T) {
	// Sentinel already exists — gate returns immediately after announcing step 1.
	r := &stubRunnerForFailureReader{
		t: t,
		scripted: map[string]struct {
			out []byte
			err error
		}{
			"test -f /tmp/.devm-install/install-all-ok": {out: []byte(""), err: nil},
			"ls /tmp/.devm-install/":                    {out: []byte(""), err: nil},
		},
	}
	sb := &sandbox.Sandbox{Name: "x", Runner: r}
	rec := &recordingReporter{}
	err := waitForPhaseSentinel(context.Background(), sb, "install", nil, 5*time.Second, 50*time.Millisecond, rec, schema.Config{})
	require.NoError(t, err)
	// Step 1 of install = bootstrap.sh (uncounted devm-internal step).
	require.Len(t, rec.steps, 1, "exactly one Step call when sentinel is immediately present")
	assert.Equal(t, "bootstrap.sh", rec.steps[0].desc)
	assert.False(t, rec.steps[0].counted, "bootstrap is not a counted (user) step")
}

func TestWaitForPhaseSentinel_AnnouncesUserStep(t *testing.T) {
	// First poll: sentinel absent, install-1.ok present → announce step 2 (first user step).
	// Second poll: sentinel present → return.
	callCount := 0
	r := &stubRunnerForFailureReader{
		t: t,
		scripted: map[string]struct {
			out []byte
			err error
		}{
			// sentinel: first call fails, second succeeds
			"test -f /tmp/.devm-install/install-all-ok": {out: []byte(""), err: nil},
			// ls: returns install-1.ok on first call
			"ls /tmp/.devm-install/": {out: []byte("install-1.ok\n"), err: nil},
		},
	}
	// Override Output to toggle sentinel on second call.
	type callKey struct{}
	_ = callKey{}

	// Use a stateful runner for this test.
	stateful := &statefulStubRunner{
		sentinelKey: "test -f /tmp/.devm-install/install-all-ok",
		lsKey:       "ls /tmp/.devm-install/",
		lsOut:       "install-1.ok\n",
		failFirst:   true,
		callCount:   &callCount,
	}
	_ = r // replaced by stateful
	sb := &sandbox.Sandbox{Name: "x", Runner: stateful}

	cfg := schema.Config{Install: []string{"apt-get install -y jq"}}
	rec := &recordingReporter{}
	err := waitForPhaseSentinel(context.Background(), sb, "install", nil, 5*time.Second, 50*time.Millisecond, rec, cfg)
	require.NoError(t, err)

	// Should have step 1 (bootstrap.sh) announced initially, then step 2 (user step).
	var descs []string
	for _, s := range rec.steps {
		descs = append(descs, s.desc)
	}
	assert.Contains(t, descs, "bootstrap.sh", "step 1 must be announced")
	assert.Contains(t, descs, "apt-get install -y jq", "step 2 (user step) must be announced")
	// The user step must be counted.
	for _, s := range rec.steps {
		if s.desc == "apt-get install -y jq" {
			assert.True(t, s.counted, "user install step must be counted")
		}
	}
}

// statefulStubRunner is a test runner that returns an error on the first
// sentinel check and success on subsequent ones, simulating a step completing.
type statefulStubRunner struct {
	mu          sync.Mutex
	sentinelKey string
	lsKey       string
	lsOut       string
	failFirst   bool
	callCount   *int
	calls       [][]string
}

func (r *statefulStubRunner) Output(name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	full := strings.Join(append([]string{name}, args...), " ")
	r.calls = append(r.calls, append([]string{name}, args...))
	if strings.Contains(full, r.sentinelKey) {
		*r.callCount++
		if r.failFirst && *r.callCount == 1 {
			return nil, errors.New("exit 1")
		}
		return []byte(""), nil
	}
	if strings.Contains(full, r.lsKey) {
		return []byte(r.lsOut), nil
	}
	return []byte(""), nil
}

func (r *statefulStubRunner) Run(name string, args ...string) error {
	_, err := r.Output(name, args...)
	return err
}

func (r *statefulStubRunner) RunStdin(stdin, name string, args ...string) error {
	return r.Run(name, args...)
}

// TestBuildShellExecArgs_Shape pins the shape both cold-start
// (RunShell) and warm reattach (runUserShell) share. Both call
// buildShellExecArgs; if either ever diverges, you'll see -w missing,
// terminfo forwarding missing, or env args in the wrong slot — all
// the bugs the consolidation was meant to prevent.
func TestBuildShellExecArgs_Shape(t *testing.T) {
	cfg := schema.Config{}
	got := buildShellExecArgs(cfg, "/repo", "sbx-a", "bash", []string{"-l"})

	// Required head shape: exec -it -w <repoRoot>
	if len(got) < 4 || got[0] != "exec" || got[1] != "-it" || got[2] != "-w" || got[3] != "/repo" {
		t.Fatalf("expected head [exec -it -w /repo], got %v", got[:4])
	}

	// Wrapper path appears (between any env args and cmdName).
	wrapperFound := false
	for _, a := range got {
		if a == "/repo/.devm/scripts/with-devm-env" {
			wrapperFound = true
			break
		}
	}
	if !wrapperFound {
		t.Errorf("expected wrapper path in argv, got %v", got)
	}

	// Tail order: <sandbox> <wrapper> <cmd> <cmdArgs...>
	if got[len(got)-1] != "-l" {
		t.Errorf("expected last arg to be cmdArg '-l', got %q", got[len(got)-1])
	}
	if got[len(got)-2] != "bash" {
		t.Errorf("expected second-to-last arg to be cmdName 'bash', got %q", got[len(got)-2])
	}
}
