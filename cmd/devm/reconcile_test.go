package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRepushRoutes_Success spins a real serviceapi.Server with the
// routes admin endpoints registered, same technique as
// startHandshakeDaemon/startStatusAllDaemon, and asserts repushRoutes
// pushes the project's routes into the daemon's table. The project has
// no pre-existing routes, so repushRoutes falls back to ModeVM; the
// service is Direct so buildRoutes(ModeVM) doesn't need a running VM
// (no tart.IP call).
func TestRepushRoutes_Success(t *testing.T) {
	cleanup := startRoutesDaemon(t)
	defer cleanup()

	cfg := schema.Config{
		Project: schema.Project{Name: "p"},
		Services: map[string]schema.Service{
			"web": {Port: 8080, Hostname: "web.test", Direct: true},
		},
	}

	repushRoutes(cfg)

	got, err := serviceapi.NewClient().ListRoutes(t.Context())
	require.NoError(t, err)
	require.Contains(t, got, "p")
	byHost := map[string]serviceapi.Route{}
	for _, r := range got["p"] {
		byHost[r.Hostname] = r
	}
	require.Contains(t, byHost, "web.test")
	assert.Equal(t, 8080, byHost["web.test"].BackendPort)
	assert.Equal(t, serviceapi.ModeVM, byHost["web.test"].Mode)
}

// TestRepushRoutes_PreservesExistingLocalMode is the regression pin
// for the "reconcile silently overrides `devm route local`" bug:
// repushRoutes must repush in the project's EXISTING mode, not a
// hardcoded ModeVM. Pre-seeds the fake daemon with a ModeLocal route
// for the project (as `devm route local` would have applied), then
// asserts the routes repushRoutes re-applies are still ModeLocal.
func TestRepushRoutes_PreservesExistingLocalMode(t *testing.T) {
	cleanup := startRoutesDaemon(t)
	defer cleanup()

	cfg := schema.Config{
		Project: schema.Project{Name: "p"},
		Services: map[string]schema.Service{
			"web": {Port: 8080, Hostname: "web.test"},
		},
	}

	seeded, err := buildRoutes(cfg, serviceapi.ModeLocal)
	require.NoError(t, err)
	require.NoError(t, serviceapi.NewClient().ApplyRoutes(t.Context(), cfg.Project.Name, seeded))

	repushRoutes(cfg)

	got, err := serviceapi.NewClient().ListRoutes(t.Context())
	require.NoError(t, err)
	require.Contains(t, got, "p")
	byHost := map[string]serviceapi.Route{}
	for _, r := range got["p"] {
		byHost[r.Hostname] = r
	}
	require.Contains(t, byHost, "web.test")
	assert.Equal(t, serviceapi.ModeLocal, byHost["web.test"].Mode, "repushRoutes must not flip a project already on ModeLocal back to ModeVM")
}

// TestRepushRoutes_DaemonDown asserts the c.Available guard makes
// repushRoutes a silent no-op when nothing is listening on the
// socket: no panic, no error to check (void function), no daemon to
// have been talked to.
func TestRepushRoutes_DaemonDown(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", t.TempDir()) // no daemon listening here

	cfg := schema.Config{
		Project: schema.Project{Name: "p"},
		Services: map[string]schema.Service{
			"web": {Port: 8080, Hostname: "web.test", Direct: true},
		},
	}

	assert.NotPanics(t, func() {
		repushRoutes(cfg)
	})
}

// startRoutesDaemon spins a real serviceapi.Server with the /routes/*
// admin endpoints registered, bound to a temp socket under
// $DEVM_RUNTIME_DIR. repushRoutes talks to it via the default
// serviceapi.NewClient(), which resolves the socket from that env
// var. Same idiom as startHandshakeDaemon (handshake_test.go) and
// startStatusAllDaemon (status_test.go).
func startRoutesDaemon(t *testing.T) func() {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sapi-rp-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("DEVM_RUNTIME_DIR", dir)

	srv := serviceapi.NewServer(serviceapi.SocketPath(), serviceapi.Build{Version: "dev"})
	routes := serviceapi.NewRoutes()
	serviceapi.RegisterRoutesHandlers(srv, routes)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, "devm.sock")); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.FileExists(t, filepath.Join(dir, "devm.sock"))

	return func() { cancel(); <-errCh }
}
