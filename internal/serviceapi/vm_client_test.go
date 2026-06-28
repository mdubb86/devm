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
	RegisterVMHandlers(srv, sup, tr)

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

	status, err := c.VMStatus(ctx, "proj-a", "")
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

// TestVMStart_MissingFields verifies that /vm/start rejects requests
// that omit required fields.
func TestVMStart_MissingFields(t *testing.T) {
	logDir := t.TempDir()
	sup := supervisor.New(logDir)
	tr := tart.New()
	tr.Path = "false"

	srv, cleanup := newTestServerWithVM(t, sup, tr)
	defer cleanup()

	c := NewClientWithSocket(srv.socketPath)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Missing vm_name.
	err := c.StartVM(ctx, VMStartRequest{ProjectID: "proj-a"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vm/start")

	// Missing project_id.
	err = c.StartVM(ctx, VMStartRequest{VMName: "proj-a-vm"})
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

// TestVMStop_NotFound verifies /vm/stop returns 500 for an unknown
// project (supervisor has no entry to stop).
func TestVMStop_NotFound(t *testing.T) {
	logDir := t.TempDir()
	sup := supervisor.New(logDir)
	tr := tart.New()
	tr.Path = "false"

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
