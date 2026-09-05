package serviceapi

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_Health_ReturnsNoErrorWhenServerReachable(t *testing.T) {
	srv, cleanup := newTestServer(t)
	defer cleanup()
	c := NewClientWithSocket(srv.socketPath)
	require.NoError(t, c.Health(context.Background()))
}

func TestClient_Version_ReturnsServerVersion(t *testing.T) {
	srv, cleanup := newTestServer(t)
	defer cleanup()
	c := NewClientWithSocket(srv.socketPath)
	v, err := c.Version(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "test-version", v)
}

func TestClient_Available_TrueWhenServerUp(t *testing.T) {
	srv, cleanup := newTestServer(t)
	defer cleanup()
	c := NewClientWithSocket(srv.socketPath)
	assert.True(t, c.Available(context.Background()))
}

func TestClient_Available_FalseWhenNoServer(t *testing.T) {
	dir := t.TempDir()
	c := NewClientWithSocket(filepath.Join(dir, "nonexistent.sock"))
	assert.False(t, c.Available(context.Background()))
}

func TestClient_CreatePopSession_RoundTrip(t *testing.T) {
	// A fake server on a temp UDS that echoes back {"mac_path":"..."}.
	// Uses /tmp directly (not t.TempDir()) to stay within macOS's
	// 104-byte sun_path limit — see newTestServer in server_test.go.
	dir, err := os.MkdirTemp("/tmp", "sapi-client-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	mux := http.NewServeMux()
	mux.HandleFunc("/pop-session", func(w http.ResponseWriter, r *http.Request) {
		var req popSessionRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, "myproj", req.Project)
		assert.Equal(t, "/tmp/thing", req.GuestPath)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(popSessionResponse{MacPath: "/scratch/xyz/thing"})
	})
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	defer ln.Close()
	go http.Serve(ln, mux)

	c := NewClientWithSocket(sock)
	macPath, err := c.CreatePopSession(context.Background(), "myproj", "/tmp/thing", false)
	require.NoError(t, err)
	assert.Equal(t, "/scratch/xyz/thing", macPath)
}
