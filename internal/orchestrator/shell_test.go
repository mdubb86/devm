package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------- fakes for RunShell tests ----------

// fakeVMAdmin is a fake VMAdminClient for unit-testing RunShell.
type fakeVMAdmin struct {
	mu          sync.Mutex
	statusResp  serviceapi.VMStatusResponse
	statusErr   error
	startCalled int
	startErr    error
}

func (f *fakeVMAdmin) VMStatus(_ context.Context, _, _ string) (serviceapi.VMStatusResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.statusResp, f.statusErr
}

func (f *fakeVMAdmin) StartVM(_ context.Context, _ serviceapi.VMStartRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCalled++
	return f.startErr
}

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

func minimalCfg() schema.Config {
	return schema.Config{
		Project:  schema.Project{ID: "x", SandboxName: "x-sbx"},
		Services: map[string]schema.Service{},
	}
}

// ---------- RunShell tests ----------

// TestRunShellWarmPath_AttachesWithoutStart verifies that when the VM is
// already running the daemon is NOT asked to start it again, and the
// user shell is spawned via tart exec.
func TestRunShellWarmPath_AttachesWithoutStart(t *testing.T) {
	repoRoot := t.TempDir()
	admin := &fakeVMAdmin{
		statusResp: serviceapi.VMStatusResponse{Present: true, Running: true},
	}

	userCmd := &stubCmd{waitErr: make(chan error, 1)}
	userCmd.waitErr <- nil
	spawner := &stubSpawner{cmdQueue: []*stubCmd{userCmd}}

	deps := ShellDeps{
		Tart:             tartPathNotNeeded(t),
		ServiceAPIClient: admin,
		UserSpawner:      spawner,
		LockPath:         filepath.Join(repoRoot, ".devm", "lock"),
	}
	rc, err := RunShell(context.Background(), deps, minimalCfg(), repoRoot, "x-sbx", "bash", nil)
	require.NoError(t, err)
	assert.Equal(t, 0, rc)

	admin.mu.Lock()
	assert.Equal(t, 0, admin.startCalled, "StartVM must NOT be called on the warm path")
	admin.mu.Unlock()
}

// TestRunShellColdPath_CallsStartAndProvisions verifies the cold-start
// sequence: StartVM is called, then waitVMReady polls exec, then the
// provisioner runs (here exercised as a no-op because tart is fake).
//
// We use a tart binary that succeeds immediately so waitVMReady returns
// right away; the provision step runs against a tart that exec's `true`
// successfully. The test only checks orchestration order, not provision
// output.
func TestRunShellColdPath_CallsStartVM(t *testing.T) {
	repoRoot := t.TempDir()
	admin := &fakeVMAdmin{
		statusResp: serviceapi.VMStatusResponse{Present: false, Running: false},
	}

	// Write a fake CA so ReadFile succeeds.
	caDir := filepath.Join(repoRoot, "ca")
	require.NoError(t, os.MkdirAll(caDir, 0o755))

	// Use a tart binary that always returns exit 0 so waitVMReady and
	// provision exec calls all succeed immediately.
	tartBin := fakeTartBin(t, repoRoot)

	userCmd := &stubCmd{waitErr: make(chan error, 1)}
	userCmd.waitErr <- nil
	spawner := &stubSpawner{cmdQueue: []*stubCmd{userCmd}}

	deps := ShellDeps{
		Tart:             tartBin,
		ServiceAPIClient: admin,
		UserSpawner:      spawner,
		LockPath:         filepath.Join(repoRoot, ".devm", "lock"),
	}

	// Point caStorageDir at our temp dir by overriding HOME.
	t.Setenv("HOME", repoRoot)
	// Write the CA root in the place caStorageDir() will look.
	caPath := filepath.Join(repoRoot, "Library", "Application Support", "devm", "ca")
	require.NoError(t, os.MkdirAll(caPath, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(caPath, "root.crt"), []byte("FAKE-CA"), 0o644))

	rc, err := RunShell(context.Background(), deps, minimalCfg(), repoRoot, "x-sbx", "bash", nil)
	require.NoError(t, err)
	assert.Equal(t, 0, rc)

	admin.mu.Lock()
	assert.Equal(t, 1, admin.startCalled, "StartVM must be called exactly once on cold start")
	admin.mu.Unlock()
}

// TestRunShellWarmPath_VMStatusError verifies that a daemon error on
// VMStatus surfaces as a RunShell error.
func TestRunShellWarmPath_VMStatusError(t *testing.T) {
	repoRoot := t.TempDir()
	admin := &fakeVMAdmin{
		statusErr: fmt.Errorf("daemon down"),
	}

	deps := ShellDeps{
		Tart:             tartPathNotNeeded(t),
		ServiceAPIClient: admin,
		UserSpawner:      &stubSpawner{},
		LockPath:         filepath.Join(repoRoot, ".devm", "lock"),
	}
	_, err := RunShell(context.Background(), deps, minimalCfg(), repoRoot, "x-sbx", "bash", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query vm status")
}

// TestRunShellColdPath_StartVMError verifies that a daemon error on
// StartVM surfaces as a RunShell error.
func TestRunShellColdPath_StartVMError(t *testing.T) {
	repoRoot := t.TempDir()
	admin := &fakeVMAdmin{
		statusResp: serviceapi.VMStatusResponse{Present: false, Running: false},
		startErr:   fmt.Errorf("clone failed"),
	}

	deps := ShellDeps{
		Tart:             tartPathNotNeeded(t),
		ServiceAPIClient: admin,
		UserSpawner:      &stubSpawner{},
		LockPath:         filepath.Join(repoRoot, ".devm", "lock"),
	}
	_, err := RunShell(context.Background(), deps, minimalCfg(), repoRoot, "x-sbx", "bash", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "start vm")
}

// tartPathNotNeeded returns a *tart.Tart whose binary is "false" —
// it will exit 1 immediately if called. Use this when the test is
// exercising a path that should never invoke tart.
func tartPathNotNeeded(t *testing.T) *tart.Tart {
	t.Helper()
	tr := tart.New()
	tr.Path = "false"
	return tr
}

// fakeTartBin writes a shell script into dir that exits 0 for all
// subcommands, and returns a *tart.Tart pointing at it.
func fakeTartBin(t *testing.T, dir string) *tart.Tart {
	t.Helper()
	bin := filepath.Join(dir, "tart-fake")
	script := "#!/bin/sh\nexec true\n"
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
	tr := tart.New()
	tr.Path = bin
	return tr
}
