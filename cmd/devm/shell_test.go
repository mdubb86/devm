package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mdubb86/devm/internal/approve"
	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/orchestrator"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/mdubb86/devm/internal/supervisor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeVMAdminClient is a minimal orchestrator.VMAdminClient fake for
// exercising orchestrator.RunAttach exactly as shellCmd's RunE calls
// it — no daemon, no VM. Only VMStatus and StartVM are exercised by
// these tests; the rest of the interface is implemented to satisfy
// the type.
type fakeVMAdminClient struct {
	status      serviceapi.VMStatusResponse
	statusErr   error
	startCalled int
}

func (f *fakeVMAdminClient) VMStatus(context.Context, string) (serviceapi.VMStatusResponse, error) {
	return f.status, f.statusErr
}
func (f *fakeVMAdminClient) StartVM(context.Context, serviceapi.VMStartRequest) (serviceapi.VMStartResponse, error) {
	f.startCalled++
	return serviceapi.VMStartResponse{}, nil
}
func (f *fakeVMAdminClient) EnforcementConfig(context.Context, string) (serviceapi.VMEnforcementConfigResponse, error) {
	return serviceapi.VMEnforcementConfigResponse{}, nil
}
func (f *fakeVMAdminClient) StopVM(context.Context, string, bool) error { return nil }
func (f *fakeVMAdminClient) ApplyIronProxy(context.Context, serviceapi.VMApplyIronProxyRequest) (serviceapi.VMApplyIronProxyResponse, error) {
	return serviceapi.VMApplyIronProxyResponse{}, nil
}
func (f *fakeVMAdminClient) VolumeSync(context.Context, string, schema.Config, string) error {
	return nil
}
func (f *fakeVMAdminClient) RepoClone(context.Context, string, schema.Config, string, int) error {
	return nil
}
func (f *fakeVMAdminClient) BeginProvisioning(context.Context, string) error { return nil }
func (f *fakeVMAdminClient) EndProvisioning(context.Context, string) error   { return nil }

// fakeTartBinary writes a shell script standing in for the `tart`
// binary, reporting `systemctl is-active devm.target` as active or
// inactive per targetActive. Every other invocation succeeds — good
// enough for the warm-attach tail (`tart exec ... bash`) to run
// without touching a real VM.
func fakeTartBinary(t *testing.T, targetActive bool) *tart.Tart {
	t.Helper()
	exitCode := "1"
	if targetActive {
		exitCode = "0"
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "tart-fake")
	script := "#!/bin/sh\ncase \"$*\" in\n  *\"is-active devm.target\"*) exit " +
		exitCode + " ;;\nesac\nexit 0\n"
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
	tr := tart.New()
	tr.Path = bin
	return tr
}

// TestShell_ErrorsWhenVMStopped proves `devm shell` never cold-starts:
// with the VM reported stopped, orchestrator.RunAttach — exactly what
// shellCmd.RunE calls — must refuse and name `devm start`, without
// ever calling StartVM.
func TestShell_ErrorsWhenVMStopped(t *testing.T) {
	admin := &fakeVMAdminClient{status: serviceapi.VMStatusResponse{Running: false}}
	deps := orchestrator.ShellDeps{
		Ident:            identity.Prod,
		Tart:             fakeTartBinary(t, true),
		ServiceAPIClient: admin,
	}

	var stderr bytes.Buffer
	_, err := orchestrator.RunAttach(context.Background(), deps, "p", t.TempDir(), "bash", nil, &stderr)
	require.Error(t, err)
	assert.Contains(t, stderr.String(), "sandbox not running")
	assert.Contains(t, stderr.String(), "devm start")
	assert.Equal(t, 0, admin.startCalled)
}

// TestShell_ErrorsWhenVMRunningButNotProvisioned proves a VM that's up
// but hasn't finished provisioning (devm.target inactive) is refused
// the same way — `devm shell` never adopts/provisions in place.
func TestShell_ErrorsWhenVMRunningButNotProvisioned(t *testing.T) {
	admin := &fakeVMAdminClient{status: serviceapi.VMStatusResponse{Running: true}}
	deps := orchestrator.ShellDeps{
		Ident:            identity.Prod,
		Tart:             fakeTartBinary(t, false),
		ServiceAPIClient: admin,
	}

	var stderr bytes.Buffer
	_, err := orchestrator.RunAttach(context.Background(), deps, "p", t.TempDir(), "bash", nil, &stderr)
	require.Error(t, err)
	assert.Contains(t, stderr.String(), "not yet provisioned")
	assert.Contains(t, stderr.String(), "devm start")
	assert.Equal(t, 0, admin.startCalled)
}

// TestStart_SurfacesApproveRequired proves the daemon's clean
// approve_required refusal reaches serviceapi.Client.StartVM's caller
// verbatim (Task 9's addition to StartVM, mirroring Client.Reconcile)
// rather than as a JSON-wrapped error body — the same client method
// `devm start` calls via orchestrator.RunShell.
func TestStart_SurfacesApproveRequired(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	store := approve.NewStore(identity.Prod)
	require.NoError(t, store.Write("p", []byte("project:\n  name: p\nenv:\n  FOO: old\n"), nil, "user"))

	macCwd := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(macCwd, "devm.yaml"),
		[]byte("project:\n  name: p\nenv:\n  FOO: new\n"), 0644))

	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "tart-fake")
	script := "#!/bin/sh\ncase \"$1\" in\n  list) echo '[{\"Name\":\"p\",\"State\":\"stopped\"}]' ;;\nesac\nexit 0\n"
	require.NoError(t, os.WriteFile(binPath, []byte(script), 0o755))
	tr := tart.New()
	tr.Path = binPath

	socket := filepath.Join(t.TempDir(), "s.sock")
	srv := serviceapi.NewServer(socket, serviceapi.Build{Version: "test"})
	serviceapi.RegisterVMHandlers(srv, identity.Prod, supervisor.New(t.TempDir()), tr, 0, serviceapi.NewProjectLocks(), nil, serviceapi.NewPopSessionStore(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx) }()
	t.Cleanup(func() { cancel(); <-errCh })

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.FileExists(t, socket)

	c := serviceapi.NewClientWithSocket(socket)
	// Generous timeout: this handler's approve-gate check does real
	// disk IO (read snapshot + devm.yaml, hash both) before refusing,
	// which under a loaded `go test ./...` run can take longer than a
	// tight deadline budgets for — this is a request-shape assertion,
	// not a timing test.
	rctx, rcancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer rcancel()
	_, err := c.StartVM(rctx, serviceapi.VMStartRequest{Name: "p", MacCwd: macCwd})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "devm.yaml (or devm.me.yaml) has changed since it was last approved.")
	assert.Contains(t, err.Error(), "devm approve")
}
