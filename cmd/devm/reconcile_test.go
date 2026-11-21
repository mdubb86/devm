package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/orchestrator"
	"github.com/mdubb86/devm/internal/reconcile"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/spf13/cobra"
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
		Services: schema.OrderedMapOf(map[string]schema.Service{
			"web": {Port: 8080, Hostname: "web.test", Direct: true},
		}),
	}

	repushRoutes(identity.Prod, cfg)

	got, err := serviceapi.NewClient(identity.Prod).ListRoutes(t.Context())
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
		Services: schema.OrderedMapOf(map[string]schema.Service{
			"web": {Port: 8080, Hostname: "web.test"},
		}),
	}

	seeded, err := buildRoutes(cfg, serviceapi.ModeLocal)
	require.NoError(t, err)
	_, err = serviceapi.NewClient(identity.Prod).ApplyRoutes(t.Context(), cfg.Project.Name, seeded)
	require.NoError(t, err)

	repushRoutes(identity.Prod, cfg)

	got, err := serviceapi.NewClient(identity.Prod).ListRoutes(t.Context())
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
	t.Setenv("HOME", t.TempDir()) // no daemon listening here

	cfg := schema.Config{
		Project: schema.Project{Name: "p"},
		Services: schema.OrderedMapOf(map[string]schema.Service{
			"web": {Port: 8080, Hostname: "web.test", Direct: true},
		}),
	}

	assert.NotPanics(t, func() {
		repushRoutes(identity.Prod, cfg)
	})
}

// TestReconcileCmd_SkipSnapshot_PropagatesToStopDeps pins that
// `devm reconcile --skip-snapshot --yes` threads the flag into StopDeps
// when the daemon reports a recreate-required change, so the
// orchestrator bypasses the lifecycle snapshot block on the teardown
// side of the recreate.
func TestReconcileCmd_SkipSnapshot_PropagatesToStopDeps(t *testing.T) {
	// Isolate the daemon handshake call: no daemon listens under this
	// HOME, which requireDaemonInSync tolerates as a nil-error no-op.
	t.Setenv("HOME", t.TempDir())

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "devm.yaml"),
		[]byte("project:\n  name: p\n"), 0o644))
	stubResolveProjectFn(t, "p", dir)

	origReconcile := runReconcileFn
	origStop := runStopFn
	origShell := runShellFlowFn
	t.Cleanup(func() {
		runReconcileFn = origReconcile
		runStopFn = origStop
		runShellFlowFn = origShell
	})

	runReconcileFn = func(_ identity.Config, _ schema.Config, _ *tart.Tart, _ string, _ orchestrator.ReconcileOptions) (int, orchestrator.ReconcileResult, error) {
		return 0, orchestrator.ReconcileResult{
			RecreateRequired: []reconcile.Change{{Kind: reconcile.KindMemoryChange}},
			Flavor:           reconcile.FlavorRestartVM,
		}, nil
	}
	var captured orchestrator.StopDeps
	var capturedMode orchestrator.Destructiveness
	runStopFn = func(_ context.Context, d orchestrator.StopDeps, _ string, mode orchestrator.Destructiveness) (int, error) {
		captured = d
		capturedMode = mode
		return 0, nil
	}
	shellCalled := false
	runShellFlowFn = func(_ *cobra.Command, _ string, _ []string) error {
		shellCalled = true
		return nil
	}

	// Reset flags after the test — cobra binds package globals.
	t.Cleanup(func() {
		reconcileSkipSnapshot = false
		reconcileYes = false
	})

	reconcileCmd.SetContext(context.Background())
	require.NoError(t, reconcileCmd.ParseFlags([]string{"--yes", "--skip-snapshot"}))
	require.NoError(t, reconcileCmd.RunE(reconcileCmd, nil))

	assert.True(t, captured.SkipSnapshot, "--skip-snapshot must set StopDeps.SkipSnapshot")
	assert.Equal(t, orchestrator.StopPreserve, capturedMode, "FlavorRestartVM must map to StopPreserve")
	assert.Equal(t, "p", captured.Cfg.Project.Name, "StopDeps.Cfg must carry the loaded project config")
	assert.True(t, shellCalled, "recreate path must invoke the shell flow after a successful stop")
}

// TestReconcileCmd_SkipSnapshotDefaultsFalse pins that omitting the
// flag leaves SkipSnapshot at its zero value — lifecycle snapshots
// still run when the recreate path fires.
func TestReconcileCmd_SkipSnapshotDefaultsFalse(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "devm.yaml"),
		[]byte("project:\n  name: p\n"), 0o644))
	stubResolveProjectFn(t, "p", dir)

	origReconcile := runReconcileFn
	origStop := runStopFn
	origShell := runShellFlowFn
	t.Cleanup(func() {
		runReconcileFn = origReconcile
		runStopFn = origStop
		runShellFlowFn = origShell
	})

	runReconcileFn = func(_ identity.Config, _ schema.Config, _ *tart.Tart, _ string, _ orchestrator.ReconcileOptions) (int, orchestrator.ReconcileResult, error) {
		return 0, orchestrator.ReconcileResult{
			RecreateRequired: []reconcile.Change{{Kind: reconcile.KindMemoryChange}},
			Flavor:           reconcile.FlavorRestartVM,
		}, nil
	}
	var captured orchestrator.StopDeps
	runStopFn = func(_ context.Context, d orchestrator.StopDeps, _ string, _ orchestrator.Destructiveness) (int, error) {
		captured = d
		return 0, nil
	}
	runShellFlowFn = func(_ *cobra.Command, _ string, _ []string) error { return nil }

	t.Cleanup(func() {
		reconcileSkipSnapshot = false
		reconcileYes = false
	})
	reconcileSkipSnapshot = false

	reconcileCmd.SetContext(context.Background())
	require.NoError(t, reconcileCmd.ParseFlags([]string{"--yes"}))
	require.NoError(t, reconcileCmd.RunE(reconcileCmd, nil))

	assert.False(t, captured.SkipSnapshot, "default must be false")
}

// startRoutesDaemon spins a real serviceapi.Server with the /routes/*
// admin endpoints registered, bound to a temp socket under a
// $HOME-scoped runtime dir. repushRoutes talks to it via the default
// serviceapi.NewClient(), which resolves the socket from
// identity.Prod.SocketPath(). Same idiom as startHandshakeDaemon
// (handshake_test.go) and startStatusAllDaemon (status_test.go).
func startRoutesDaemon(t *testing.T) func() {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sapi-rp-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("HOME", dir)

	_, err = serviceapi.EnsureRuntimeDir(identity.Prod)
	require.NoError(t, err)
	socket := identity.Prod.SocketPath()
	srv := serviceapi.NewServer(socket, serviceapi.Build{Version: "dev"})
	routes := serviceapi.NewRoutes(identity.Prod.TLD)
	proxy := serviceapi.NewProxyServer(identity.Prod, routes, nil)
	serviceapi.RegisterRoutesHandlers(srv, identity.Prod, routes, proxy)

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

	return func() { cancel(); <-errCh }
}
