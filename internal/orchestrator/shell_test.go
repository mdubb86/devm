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
	"github.com/mdubb86/devm/internal/secret"
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
	stopCalled  int
	stopErr     error
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

func (f *fakeVMAdmin) ApplyEgressEnforcement(_ context.Context, _, _ string) error {
	return nil
}

func (f *fakeVMAdmin) StopVM(_ context.Context, _, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopCalled++
	return f.stopErr
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
		Project:  schema.Project{ID: "x", VMName: "x-vm"},
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

// TestRunShellColdPath_PostInstallFail_KeepsVM verifies that a
// service-phase failure (enable + start services, etc.) leaves the VM
// running so the user can debug — install failures still tear down.
func TestRunShellColdPath_PostInstallFail_KeepsVM(t *testing.T) {
	repoRoot := t.TempDir()
	admin := &fakeVMAdmin{
		statusResp: serviceapi.VMStatusResponse{Present: false, Running: false},
	}
	// Fail on systemctl is-active — that only fires from
	// enable+start-services in the health poll. Everything before succeeds.
	tartBin, logPath := fakeTartBinFailingAt(t, repoRoot, "is-active")

	// Provision needs at least one declared service to hit is-active.
	cfg := minimalCfg()
	cfg.Services = map[string]schema.Service{
		"broken": {Exec: []string{"/bin/false"}, Restart: "no"},
	}

	spawner := &stubSpawner{}
	deps := ShellDeps{
		Tart:             tartBin,
		ServiceAPIClient: admin,
		UserSpawner:      spawner,
	}
	t.Setenv("HOME", repoRoot)
	caPath := filepath.Join(repoRoot, "Library", "Application Support", "devm", "ca")
	require.NoError(t, os.MkdirAll(caPath, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(caPath, "root.crt"), []byte("FAKE-CA"), 0o644))

	_, err := RunShell(context.Background(), deps, cfg, repoRoot, "x-sbx", "bash", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "enable + start services")

	admin.mu.Lock()
	assert.Equal(t, 0, admin.stopCalled, "StopVM must NOT be called on post-install failure")
	admin.mu.Unlock()

	if logBytes, err := os.ReadFile(logPath); err == nil {
		assert.NotContains(t, string(logBytes), "delete x-sbx",
			"tart delete must NOT run on post-install failure — VM is worth debugging in place")
	}
}

// TestRunShellColdPath_ProvisionFail_TearsDownVM verifies Bug B: when a
// cold-start step after StartVM fails, RunShell asks the daemon to stop
// the VM AND invokes `tart delete` so no zombie VM is left behind.
func TestRunShellColdPath_ProvisionFail_TearsDownVM(t *testing.T) {
	repoRoot := t.TempDir()
	admin := &fakeVMAdmin{
		statusResp: serviceapi.VMStatusResponse{Present: false, Running: false},
	}

	// Fail on the CA install shell script — first provision-step that
	// contains "base64" in its argv. waitVMReady (`true`) still succeeds.
	tartBin, logPath := fakeTartBinFailingAt(t, repoRoot, "base64")

	spawner := &stubSpawner{}
	deps := ShellDeps{
		Tart:             tartBin,
		ServiceAPIClient: admin,
		UserSpawner:      spawner,
	}
	t.Setenv("HOME", repoRoot)
	caPath := filepath.Join(repoRoot, "Library", "Application Support", "devm", "ca")
	require.NoError(t, os.MkdirAll(caPath, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(caPath, "root.crt"), []byte("FAKE-CA"), 0o644))

	_, err := RunShell(context.Background(), deps, minimalCfg(), repoRoot, "x-sbx", "bash", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "provision")

	// Bug B assertions.
	admin.mu.Lock()
	assert.Equal(t, 1, admin.stopCalled, "StopVM must be called on provision failure")
	admin.mu.Unlock()

	logBytes, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Contains(t, string(logBytes), "delete x-sbx",
		"tart delete <vm> must run on provision failure so the VM disk is destroyed")
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

// fakeTartBinFailingAt writes a shell script that exits 1 on any argv
// containing `failMarker`, and records every invocation to a log file.
// Returns the *tart.Tart and the path to the invocation log.
func fakeTartBinFailingAt(t *testing.T, dir, failMarker string) (*tart.Tart, string) {
	t.Helper()
	bin := filepath.Join(dir, "tart-fake-failing")
	logPath := filepath.Join(dir, "tart-invocations.log")
	script := fmt.Sprintf(`#!/bin/sh
echo "$*" >> %q
for arg in "$@"; do
  case "$arg" in
    *%s*) exit 1 ;;
  esac
done
exit 0
`, logPath, failMarker)
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
	tr := tart.New()
	tr.Path = bin
	return tr, logPath
}

// ---------- resolveSecretBindings tests ----------

func secretRef(name string) schema.EnvValue {
	return schema.EnvValue{Secret: &schema.SecretRef{Name: name}}
}

func TestResolveSecretBindings(t *testing.T) {
	t.Run("secret_with_host_scope", func(t *testing.T) {
		// A secret named under a host allow-entry comes back with Hosts populated.
		be := secret.NewFake()
		require.NoError(t, be.Set("proj/gh", "token123"))

		cfg := schema.Config{
			Project: schema.Project{ID: "proj"},
			Env:     map[string]schema.EnvValue{"TOKEN": secretRef("gh")},
			Network: schema.Network{
				Allow: []schema.AllowEntry{
					{Host: "api.github.com", Secrets: []string{"gh"}},
				},
			},
		}

		bindings, err := resolveSecretBindings(cfg, be)
		require.NoError(t, err)
		require.Len(t, bindings, 1)
		assert.Equal(t, "gh", bindings[0].Name)
		assert.Equal(t, "token123", bindings[0].Value)
		assert.Equal(t, []string{"api.github.com"}, bindings[0].Hosts)
	})

	t.Run("secret_with_no_host_scope", func(t *testing.T) {
		// A secret referenced in env but bound to NO allow-entry host comes
		// back with empty/nil Hosts (iron-proxy never injects it).
		be := secret.NewFake()
		require.NoError(t, be.Set("proj/mytoken", "secret_value"))

		cfg := schema.Config{
			Project: schema.Project{ID: "proj"},
			Env:     map[string]schema.EnvValue{"MY_TOKEN": secretRef("mytoken")},
			Network: schema.Network{
				Allow: []schema.AllowEntry{
					{Host: "example.com"}, // no secrets listed
				},
			},
		}

		bindings, err := resolveSecretBindings(cfg, be)
		require.NoError(t, err)
		require.Len(t, bindings, 1)
		assert.Equal(t, "mytoken", bindings[0].Name)
		assert.Equal(t, "secret_value", bindings[0].Value)
		assert.Empty(t, bindings[0].Hosts)
	})

	t.Run("missing_keychain_entry_returns_error", func(t *testing.T) {
		// A !secret whose keychain entry is missing → error mentioning the name.
		be := secret.NewFake()
		// Deliberately do NOT seed "proj/missing".

		cfg := schema.Config{
			Project: schema.Project{ID: "proj"},
			Env:     map[string]schema.EnvValue{"TOKEN": secretRef("missing")},
		}

		_, err := resolveSecretBindings(cfg, be)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing")
	})

	t.Run("secret_under_two_hosts_gets_both_sorted", func(t *testing.T) {
		// A secret named under two allow entries comes back with both hosts sorted.
		be := secret.NewFake()
		require.NoError(t, be.Set("proj/tok", "val"))

		cfg := schema.Config{
			Project: schema.Project{ID: "proj"},
			Env:     map[string]schema.EnvValue{"T": secretRef("tok")},
			Network: schema.Network{
				Allow: []schema.AllowEntry{
					{Host: "z.example.com", Secrets: []string{"tok"}},
					{Host: "a.example.com", Secrets: []string{"tok"}},
				},
			},
		}

		bindings, err := resolveSecretBindings(cfg, be)
		require.NoError(t, err)
		require.Len(t, bindings, 1)
		assert.Equal(t, []string{"a.example.com", "z.example.com"}, bindings[0].Hosts)
	})

	t.Run("no_secrets_returns_nil", func(t *testing.T) {
		be := secret.NewFake()
		cfg := schema.Config{
			Project: schema.Project{ID: "proj"},
			Env:     map[string]schema.EnvValue{"PLAIN": {Literal: "value"}},
		}
		bindings, err := resolveSecretBindings(cfg, be)
		require.NoError(t, err)
		assert.Nil(t, bindings)
	})
}
