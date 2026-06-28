package serviceapi

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestServerWithRoutes returns a Server with routes handlers
// registered, bound to a temp socket. Same approach as newTestServer
// in server_test.go but with route admin endpoints wired up.
func newTestServerWithRoutes(t *testing.T) (*Server, *Routes, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sapi-r-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })

	socket := filepath.Join(dir, "s.sock")
	srv := NewServer(socket, Build{Version: "test-version"})
	routes := NewRoutes()
	RegisterRoutesHandlers(srv, routes)

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

	return srv, routes, func() { cancel(); <-errCh }
}

func TestClient_ApplyAndListRoutes_Roundtrip(t *testing.T) {
	srv, _, cleanup := newTestServerWithRoutes(t)
	defer cleanup()

	c := NewClientWithSocket(srv.socketPath)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	in := []Route{
		{Hostname: "app.test", BackendPort: 51001, Mode: ModeVM},
		{Hostname: "api.test", BackendPort: 51002, Mode: ModeVM},
	}
	require.NoError(t, c.ApplyRoutes(ctx, "p1", in))

	got, err := c.ListRoutes(ctx)
	require.NoError(t, err)
	require.Contains(t, got, "p1")
	assert.Len(t, got["p1"], 2)
}

func TestClient_RemoveRoutes(t *testing.T) {
	srv, _, cleanup := newTestServerWithRoutes(t)
	defer cleanup()

	c := NewClientWithSocket(srv.socketPath)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	require.NoError(t, c.ApplyRoutes(ctx, "p1",
		[]Route{{Hostname: "x.test", BackendPort: 1, Mode: ModeVM}}))
	require.NoError(t, c.RemoveRoutes(ctx, "p1"))

	got, err := c.ListRoutes(ctx)
	require.NoError(t, err)
	assert.NotContains(t, got, "p1")
}
