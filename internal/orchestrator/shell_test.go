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
	killed  bool
	pid     int
}

func (c *stubCmd) Wait() error { return <-c.waitErr }
func (c *stubCmd) Kill() error {
	c.killed = true
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
	probeOut string
	portsOut string
}

func (r *stateRunner) Output(name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	all := append([]string{name}, args...)
	r.calls = append(r.calls, all)
	joined := strings.Join(all, " ")
	switch {
	case strings.Contains(joined, "sbx ls"):
		return []byte("SANDBOX  STATUS\nx-sbx    " + r.lsStatus + "\n"), nil
	case strings.Contains(joined, "ports") && strings.Contains(joined, "--json"):
		if r.portsOut != "" {
			return []byte(r.portsOut), nil
		}
		return []byte("[]"), nil
	case strings.Contains(joined, "sh") && strings.Contains(joined, "-c"):
		// Sessions probe script.
		return []byte(r.probeOut), nil
	}
	return []byte(""), nil
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
		Spawner:  spawner,
		Runner:   runner,
		LockPath: filepath.Join(repoRoot, ".devm/lock"),
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

func TestRunShellColdStartHappyPath(t *testing.T) {
	repoRoot := t.TempDir()

	runner := &stateRunner{
		lsStatus: "stopped",
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
		runner.lsStatus = "running"
		runner.mu.Unlock()
	}()

	deps := ShellDeps{
		Spawner:        spawner,
		Runner:         runner,
		LockPath:       filepath.Join(repoRoot, ".devm/lock"),
		WaitForRunning: 2 * time.Second,
		WaitForPty:     2 * time.Second,
		PollInterval:   20 * time.Millisecond,
	}
	rc, err := RunShell(context.Background(), deps, minimalCfg(), repoRoot, "x-sbx", "bash", nil)
	require.NoError(t, err)
	assert.Equal(t, 0, rc)

	require.GreaterOrEqual(t, len(spawner.started), 2)
	assert.Contains(t, strings.Join(spawner.started[0], " "), "sbx run")
	assert.Contains(t, strings.Join(spawner.started[1], " "), "sbx exec")

	// runCmd must have been killed after the user shell came up.
	assert.True(t, runCmd.killed, "sbx run subprocess should have been killed once user shell came up")
}

func TestRunShellWaitForRunningTimesOut(t *testing.T) {
	repoRoot := t.TempDir()
	runner := &stateRunner{lsStatus: "stopped"} // never flips
	runCmd := &stubCmd{waitErr: make(chan error, 1)}
	spawner := &stubSpawner{cmdQueue: []*stubCmd{runCmd}}

	deps := ShellDeps{
		Spawner:        spawner,
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
