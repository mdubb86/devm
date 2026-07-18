package serviceapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/supervisor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestServerWithVM returns a Server with VM handlers registered
// on a temp socket. sup and tr are the live collaborators for the
// handler (callers may substitute a real or stub supervisor/tart).
func newTestServerWithVM(t *testing.T, sup *supervisor.Supervisor, tr *tart.Tart) (*Server, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sapi-vm-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })

	socket := filepath.Join(dir, "s.sock")
	srv := NewServer(socket, Build{Version: "test-version"})
	RegisterVMHandlers(srv, sup, tr, nil, 0, NewProjectLocks())

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.FileExists(t, socket)

	return srv, func() { cancel(); <-errCh }
}

// TestVMStatus_Empty verifies that /vm/status returns present=false for
// an unknown project_id (no supervisor entry).
func TestVMStatus_Empty(t *testing.T) {
	logDir := t.TempDir()
	sup := supervisor.New(logDir)
	tr := tart.New()
	tr.Path = "false" // don't actually run tart; IP won't be called in this test

	srv, cleanup := newTestServerWithVM(t, sup, tr)
	defer cleanup()

	c := NewClientWithSocket(srv.socketPath)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	status, err := c.VMStatus(ctx, "proj-a")
	require.NoError(t, err)
	assert.False(t, status.Present)
	assert.False(t, status.Running)
	assert.Equal(t, 0, status.PID)
	assert.Empty(t, status.IP)
}

// TestVMStatus_MissingProjectID verifies the handler rejects requests
// without a project_id query parameter.
func TestVMStatus_MissingProjectID(t *testing.T) {
	logDir := t.TempDir()
	sup := supervisor.New(logDir)
	tr := tart.New()
	tr.Path = "false"

	srv, cleanup := newTestServerWithVM(t, sup, tr)
	defer cleanup()

	// Hit the raw endpoint without project_id.
	req, err := http.NewRequest("GET", "http://localhost/vm/status", nil)
	require.NoError(t, err)
	client := NewClientWithSocket(srv.socketPath)
	resp, err := client.httpClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// TestVMStart_MissingName verifies that /vm/start rejects a request
// that omits the required name.
func TestVMStart_MissingName(t *testing.T) {
	logDir := t.TempDir()
	sup := supervisor.New(logDir)
	tr := tart.New()
	tr.Path = "false"

	srv, cleanup := newTestServerWithVM(t, sup, tr)
	defer cleanup()

	c := NewClientWithSocket(srv.socketPath)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Missing name → 400.
	err := c.StartVM(ctx, VMStartRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vm/start")
}

// TestVMStop_MissingProjectID verifies /vm/stop rejects empty project_id.
func TestVMStop_MissingProjectID(t *testing.T) {
	logDir := t.TempDir()
	sup := supervisor.New(logDir)
	tr := tart.New()
	tr.Path = "false"

	srv, cleanup := newTestServerWithVM(t, sup, tr)
	defer cleanup()

	c := NewClientWithSocket(srv.socketPath)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := c.StopVM(ctx, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vm/stop")
}

// TestVMStop_WithVMName_PowersOffGuest verifies /vm/stop shuts the guest
// down cleanly from the inside (`tart exec <name> sudo systemctl poweroff`)
// before the supervisor force-terminates it — `tart stop` crashes the guest
// (cirruslabs/tart#582, #659), leaving docker `--restart` containers stuck
// "created" and dropping in-flight disk writes across a restart.
func TestVMStop_WithVMName_PowersOffGuest(t *testing.T) {
	logDir := t.TempDir()
	sup := supervisor.New(logDir)

	// Record tart invocations. `list` reports no running VM so the graceful
	// stop's poll returns immediately; other calls are logged.
	dir := t.TempDir()
	logPath := filepath.Join(dir, "tart-log")
	bin := filepath.Join(dir, "tart-fake")
	script := "#!/bin/sh\ncase \"$1\" in\n  list) echo '[]' ;;\n  *) echo \"$*\" >> " + logPath + " ;;\nesac\nexit 0\n"
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
	tr := tart.New()
	tr.Path = bin

	srv, cleanup := newTestServerWithVM(t, sup, tr)
	defer cleanup()

	c := NewClientWithSocket(srv.socketPath)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err := c.StopVM(ctx, "proj-a")
	require.NoError(t, err)

	logBytes, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Contains(t, string(logBytes), "exec proj-a sudo systemctl poweroff",
		"handler must power the guest off from the inside for a clean shutdown")
}

// TestVMStop_NotFound verifies /vm/stop is idempotent for an unknown
// project (supervisor has no entry to stop).
func TestVMStop_NotFound(t *testing.T) {
	logDir := t.TempDir()
	sup := supervisor.New(logDir)
	// `list` reports no running VM so the graceful stop's poll returns at
	// once; everything else is a harmless no-op.
	bin := filepath.Join(t.TempDir(), "tart-fake")
	script := "#!/bin/sh\ncase \"$1\" in\n  list) echo '[]' ;;\nesac\nexit 0\n"
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
	tr := tart.New()
	tr.Path = bin

	srv, cleanup := newTestServerWithVM(t, sup, tr)
	defer cleanup()

	c := NewClientWithSocket(srv.socketPath)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// StopVM on a project the supervisor doesn't know about is
	// idempotent — both the iron-proxy and VM stops treat
	// supervisor.ErrNotFound as success so re-tearing-down or
	// stopping a project that was never started is silent.
	err := c.StopVM(ctx, "nonexistent-project")
	require.NoError(t, err)
}

// TestVMStatus_MethodNotAllowed verifies that non-GET requests to
// /vm/status are rejected.
func TestVMStatus_MethodNotAllowed(t *testing.T) {
	logDir := t.TempDir()
	sup := supervisor.New(logDir)
	tr := tart.New()
	tr.Path = "false"

	srv, cleanup := newTestServerWithVM(t, sup, tr)
	defer cleanup()

	c := NewClientWithSocket(srv.socketPath)

	body, _ := json.Marshal(map[string]string{"project_id": "p1"})
	resp, err := c.post(context.Background(), "/vm/status", body)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}

// TestVMStart_MethodNotAllowed verifies that non-POST requests to
// /vm/start are rejected.
func TestVMStart_MethodNotAllowed(t *testing.T) {
	logDir := t.TempDir()
	sup := supervisor.New(logDir)
	tr := tart.New()
	tr.Path = "false"

	srv, cleanup := newTestServerWithVM(t, sup, tr)
	defer cleanup()

	c := NewClientWithSocket(srv.socketPath)

	req, err := http.NewRequestWithContext(context.Background(), "GET", "http://localhost/vm/start", nil)
	require.NoError(t, err)
	resp, err := c.httpClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}

// TestClientReconcile_RoundTrip verifies Client.Reconcile against a
// real daemon socket: a live-bucket env change round-trips through
// POST /vm/reconcile and comes back classified as Applied, with
// TeardownRequired empty.
func TestClientReconcile_RoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	createTestCA(t)

	oldCfg := schema.Config{
		Project: schema.Project{Name: "p"},
		Env:     map[string]schema.EnvValue{"FOO": {Literal: "old"}},
	}
	require.NoError(t, WriteStateSnapshot("p", StateSnapshot{Cfg: oldCfg}))
	newCfg := oldCfg
	newCfg.Env = map[string]schema.EnvValue{"FOO": {Literal: "new"}}

	dir, err := os.MkdirTemp("/tmp", "sapi-reconcile-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s.sock")

	srv := NewServer(socket, Build{Version: "test-version"})
	RegisterReconcileHandler(srv, NewProjectLocks(), &fakeApply{}, &fakeTartList{running: true, vmName: "p"}, supervisor.New(""))

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

	c := NewClientWithSocket(socket)
	rctx, rcancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer rcancel()

	resp, err := c.Reconcile(rctx, VMReconcileRequest{
		Name: "p", Cfg: newCfg, WorkspaceHostPath: "/tmp/repo",
	})
	require.NoError(t, err)
	require.Len(t, resp.Applied, 1)
	assert.Equal(t, "new", resp.Applied[0].New)
	assert.Empty(t, resp.TeardownRequired)
}

// TestClientReconcile_MissingFields verifies /vm/reconcile rejects a
// request lacking project_id/vm_name, and that the error surfaces the
// endpoint name for easy grepping.
func TestClientReconcile_MissingFields(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	dir, err := os.MkdirTemp("/tmp", "sapi-reconcile-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s.sock")

	srv := NewServer(socket, Build{})
	RegisterReconcileHandler(srv, NewProjectLocks(), &fakeApply{}, &fakeTartList{running: true, vmName: "p"}, supervisor.New(""))

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx) }()
	t.Cleanup(func() { cancel(); <-errCh })

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, statErr := os.Stat(socket); statErr == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.FileExists(t, socket)

	c := NewClientWithSocket(socket)
	_, err = c.Reconcile(context.Background(), VMReconcileRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vm/reconcile")
}

// TestVMStop_MethodNotAllowed verifies that non-POST requests to
// /vm/stop are rejected.
func TestVMStop_MethodNotAllowed(t *testing.T) {
	logDir := t.TempDir()
	sup := supervisor.New(logDir)
	tr := tart.New()
	tr.Path = "false"

	srv, cleanup := newTestServerWithVM(t, sup, tr)
	defer cleanup()

	c := NewClientWithSocket(srv.socketPath)

	req, err := http.NewRequestWithContext(context.Background(), "GET", "http://localhost/vm/stop", nil)
	require.NoError(t, err)
	resp, err := c.httpClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}

// TestClientEnforcementConfig_ReadsResponse verifies GET
// /vm/enforcement-config returns only the guest-side timesyncd config now
// — egress allow-listing and DNS are enforced by softnet over the control
// socket (POST /vm/apply-egress-enforcement), so NftRuleset and
// DnsmasqScript are always empty.
func TestClientEnforcementConfig_ReadsResponse(t *testing.T) {
	logDir := t.TempDir()
	sup := supervisor.New(logDir)
	tr := tart.New()
	tr.Path = "false"

	srv, cleanup := newTestServerWithVM(t, sup, tr)
	defer cleanup()
	t.Cleanup(func() { ironProxyState.del("proj-enf") })

	ironProxyState.put("proj-enf", ironProxyInfo{
		MacHost: "192.168.64.1", HTTPPort: 8080, HTTPSPort: 8443, DNSPort: 8053,
	})

	c := NewClientWithSocket(srv.socketPath)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := c.EnforcementConfig(ctx, "proj-enf")
	require.NoError(t, err)
	assert.Empty(t, resp.NftRuleset)
	assert.Empty(t, resp.DnsmasqScript)
	assert.Contains(t, resp.TimesyncdScript, "/etc/systemd/timesyncd.conf.d/devm.conf")
}

// TestClientEnforcementConfig_MissingProjectState verifies the endpoint
// 404/412s (surfaced as a Client error) when /vm/start was never called
// for the project — there's no MAC_HOST/ports to compute config from.
func TestClientEnforcementConfig_MissingProjectState(t *testing.T) {
	logDir := t.TempDir()
	sup := supervisor.New(logDir)
	tr := tart.New()
	tr.Path = "false"

	srv, cleanup := newTestServerWithVM(t, sup, tr)
	defer cleanup()

	c := NewClientWithSocket(srv.socketPath)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := c.EnforcementConfig(ctx, "nonexistent-project")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "enforcement-config")
}

// TestClientApplyIronProxy_ReadsResponse verifies Client.ApplyIronProxy
// round-trips a request to the daemon and unmarshals the response correctly.
func TestClientApplyIronProxy_ReadsResponse(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "sapi-apply-iron-proxy-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })

	sock := filepath.Join(dir, "s.sock")
	srv := NewServer(sock, Build{})
	// Fake handler that returns applied=true, revived=false, vm_running=true.
	srv.mux.HandleFunc("/vm/apply-iron-proxy", func(w http.ResponseWriter, r *http.Request) {
		var req VMApplyIronProxyRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, "p", req.Name)
		writeJSON(w, VMApplyIronProxyResponse{Applied: true, Revived: false, VMRunning: true})
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.FileExists(t, sock)

	c := NewClientWithSocket(sock)
	resp, err := c.ApplyIronProxy(context.Background(), VMApplyIronProxyRequest{Name: "p",
		Allowlist: []string{"a.com"},
	})
	require.NoError(t, err)
	assert.True(t, resp.Applied)
	assert.False(t, resp.Revived)
	assert.True(t, resp.VMRunning)
}
