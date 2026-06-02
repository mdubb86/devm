package orchestrator

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mtwaage/devm/internal/sandbox"
	"github.com/mtwaage/devm/internal/schema"
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
		Project:  schema.Project{ID: "x", SandboxName: "x-sbx", HostnameApex: "x.local", PortOffset: 60000},
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
		"api": {Canonical: 8080, Env: map[string]string{"LOG_LEVEL": "info"}},
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

	// The user shell's sbx exec args must include the injected service
	// env var (uppercased service-name prefix).
	require.Len(t, spawner.started, 1)
	joined := strings.Join(spawner.started[0], " ")
	assert.Contains(t, joined, "API_LOG_LEVEL=info", "service env must be injected into the shell")
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
	// Anchor must be wrapped in nohup so it inherits SIGHUP=SIG_IGN
	// across the exec — sandbox survives terminal-close cascades.
	// Pinned by e2e/test_sbx_anchor_10_terminal_close.py (ignhup_only).
	assert.Equal(t, "nohup", spawner.started[0][0],
		"anchor argv[0] must be `nohup` so it ignores SIGHUP")
	assert.Equal(t, "sbx", spawner.started[0][1],
		"argv[1] must be `sbx` (nohup execvps into it)")
	assert.Equal(t, "run", spawner.started[0][2],
		"argv[2] must be `run`")
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
		"both anchor (sbx run, wrapped in nohup) and user shell (sbx exec) must be spawned")
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
	expectedArgs := []string{"nohup", "sbx", "run", "--kit", filepath.Join(repoRoot, ".devm"), "x-sbx"}
	assert.Equal(t, expectedArgs, spawner.started[0],
		"restart path argv should be exactly: nohup sbx run --kit <kitdir> <sandbox-name>")
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
