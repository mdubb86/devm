package serviceapi

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mdubb86/devm/internal/identity"
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
	proxy := NewProxyServer(identity.Prod, routes, nil)
	RegisterRoutesHandlers(srv, routes, proxy)

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

	// vm-mode non-direct routes require an allocated projectIP — the
	// handler substitutes it into BackendHost (see routes.go).
	ironProxyState.put("p1", projectInfo{ProjectIP: "127.42.0.1"})
	t.Cleanup(func() { ironProxyState.del("p1") })

	c := NewClientWithSocket(srv.socketPath)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	in := []Route{
		{Hostname: "app.test", BackendPort: 51001, Mode: ModeVM},
		{Hostname: "api.test", BackendPort: 51002, Mode: ModeVM},
	}
	_, err := c.ApplyRoutes(ctx, "p1", in)
	require.NoError(t, err)

	got, err := c.ListRoutes(ctx)
	require.NoError(t, err)
	require.Contains(t, got, "p1")
	assert.Len(t, got["p1"], 2)
}

// TestApplyRoutes_ReturnsResolvedRoutes verifies ApplyRoutes surfaces the
// daemon's resolved routes (BackendHost substituted for vm-mode non-direct
// routes) instead of just an error.
func TestApplyRoutes_ReturnsResolvedRoutes(t *testing.T) {
	srv, _, cleanup := newTestServerWithRoutes(t)
	defer cleanup()

	ironProxyState.put("proj", projectInfo{ProjectIP: "127.42.0.7"})
	t.Cleanup(func() { ironProxyState.del("proj") })

	c := NewClientWithSocket(srv.socketPath)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	got, err := c.ApplyRoutes(ctx, "proj", []Route{
		{Hostname: "api.test", BackendPort: 8080, Mode: ModeVM},
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "127.42.0.7", got[0].BackendHost)
}

// TestRoutingStatusFromDaemon_DialUsesBackendHost verifies a resolved
// vm-mode route's Dial reflects its BackendHost, not a hardcoded
// "localhost".
func TestRoutingStatusFromDaemon_DialUsesBackendHost(t *testing.T) {
	srv, routes, cleanup := newTestServerWithRoutes(t)
	defer cleanup()

	require.NoError(t, routes.Apply("proj", []Route{
		{Hostname: "api.test", BackendHost: "127.42.0.7", BackendPort: 8080, Mode: ModeVM},
	}))

	c := NewClientWithSocket(srv.socketPath)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	rs, err := c.RoutingStatusFromDaemon(ctx)
	require.NoError(t, err)
	require.Len(t, rs.Routes, 1)
	assert.Equal(t, "127.42.0.7:8080", rs.Routes[0].Dial)
}

// TestRoutingStatusFromDaemon_CountsLANExposedRoutes verifies
// LANExposedCount reflects the number of ExposeHost routes across all
// projects — the signal `devm status`'s LAN listener row renders from.
func TestRoutingStatusFromDaemon_CountsLANExposedRoutes(t *testing.T) {
	srv, routes, cleanup := newTestServerWithRoutes(t)
	defer cleanup()

	require.NoError(t, routes.Apply("proj-a", []Route{
		{Hostname: "api.test", BackendPort: 8080, Mode: ModeLocal, ExposeHost: true},
		{Hostname: "internal.test", BackendPort: 9090, Mode: ModeLocal},
	}))
	require.NoError(t, routes.Apply("proj-b", []Route{
		{Hostname: "web.test", BackendPort: 3000, Mode: ModeLocal, ExposeHost: true},
	}))

	c := NewClientWithSocket(srv.socketPath)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	rs, err := c.RoutingStatusFromDaemon(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, rs.LANExposedCount)
}

func TestClient_RemoveRoutes(t *testing.T) {
	srv, _, cleanup := newTestServerWithRoutes(t)
	defer cleanup()

	// vm-mode non-direct routes require an allocated projectIP — the
	// handler substitutes it into BackendHost (see routes.go).
	ironProxyState.put("p1", projectInfo{ProjectIP: "127.42.0.2"})
	t.Cleanup(func() { ironProxyState.del("p1") })

	c := NewClientWithSocket(srv.socketPath)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := c.ApplyRoutes(ctx, "p1",
		[]Route{{Hostname: "x.test", BackendPort: 1, Mode: ModeVM}})
	require.NoError(t, err)
	require.NoError(t, c.RemoveRoutes(ctx, "p1"))

	got, err := c.ListRoutes(ctx)
	require.NoError(t, err)
	assert.NotContains(t, got, "p1")
}
