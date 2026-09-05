package main

import (
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

// fakeStatusTart reports a fixed running-VM set without shelling out
// to `tart`, mirroring fakeTartList in internal/serviceapi.
type fakeStatusTart struct{ running map[string]bool }

func (f *fakeStatusTart) List(ctx context.Context) ([]tart.VM, error) {
	vms := make([]tart.VM, 0, len(f.running))
	for name, running := range f.running {
		vms = append(vms, tart.VM{Name: name, Running: running})
	}
	return vms, nil
}

// startStatusAllDaemon spins a real serviceapi.Server with only
// /status/all registered, bound to a temp socket — same technique
// startHandshakeDaemon uses in handshake_test.go.
func startStatusAllDaemon(t *testing.T, running map[string]bool) func() {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sapi-sa-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("HOME", dir)

	_, err = serviceapi.EnsureRuntimeDir(identity.Prod)
	require.NoError(t, err)
	socket := identity.Prod.SocketPath()
	srv := serviceapi.NewServer(socket, serviceapi.Build{Version: "dev"})
	sup := supervisor.New(t.TempDir())
	serviceapi.RegisterStatusAllHandler(srv, identity.Prod, sup, &fakeStatusTart{running: running}, nil)

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

// TestStatusAll_ClientRoundTrip exercises the full `devm status --all`
// pipeline the CLI drives: Client.StatusAll against a real daemon,
// then the same exit-decision helper RunE calls. Doesn't invoke RunE
// itself since that os.Exit()s on drift — see anyProjectNeedsReconcile
// for the unit-tested decision logic.
func TestStatusAll_ClientRoundTrip(t *testing.T) {
	cleanup := startStatusAllDaemon(t, map[string]bool{"p": true})
	defer cleanup()

	require.NoError(t, serviceapi.WriteStateSnapshot(identity.Prod, "p", serviceapi.StateSnapshot{
		Cfg: schema.Config{Project: schema.Project{Name: "p"}},
	}))

	rows, err := serviceapi.NewClient(identity.Prod).StatusAll(context.Background())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "p", rows[0].Name)
	assert.True(t, rows[0].VMRunning)
	assert.Equal(t, serviceapi.ProxyMissing, rows[0].Proxy.Status)
	assert.True(t, anyProjectNeedsReconcile(rows))
}

// TestStatus_InvalidConfigSurfacesError: a devm.yaml that exists but
// fails validation must error out of `devm status`, not silently fall
// back to daemon-only mode (which prints the misleading "no devm.yaml
// in cwd" line).
func TestStatus_InvalidConfigSurfacesError(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "devm.yaml"),
		[]byte("project:\n  name: p\n  vm_name: legacy\n"), 0o644))
	t.Chdir(dir)
	t.Setenv("HOME", t.TempDir())

	statusCmd.SetContext(context.Background())
	err := statusCmd.RunE(statusCmd, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vm_name")
}

// TestAnyProjectNeedsReconcile covers the exit-4 decision `devm status
// --all` makes: reconcile is required only when a *running* VM's
// iron-proxy is unhealthy — stopped VMs are excluded, matching
// FormatStatusAllText's "—" columns for stopped rows.
func TestAnyProjectNeedsReconcile(t *testing.T) {
	cases := []struct {
		name string
		rows []serviceapi.ProjectStatus
		want bool
	}{
		{
			name: "empty",
			rows: nil,
			want: false,
		},
		{
			name: "all ok",
			rows: []serviceapi.ProjectStatus{
				{Name: "a", VMRunning: true, Proxy: serviceapi.ProxyHealth{Status: serviceapi.ProxyOK}},
			},
			want: false,
		},
		{
			name: "running missing",
			rows: []serviceapi.ProjectStatus{
				{Name: "a", VMRunning: true, Proxy: serviceapi.ProxyHealth{Status: serviceapi.ProxyMissing}},
			},
			want: true,
		},
		{
			name: "running stale",
			rows: []serviceapi.ProjectStatus{
				{Name: "a", VMRunning: true, Proxy: serviceapi.ProxyHealth{Status: serviceapi.ProxyStale}},
			},
			want: true,
		},
		{
			name: "stopped missing is excluded",
			rows: []serviceapi.ProjectStatus{
				{Name: "a", VMRunning: false, Proxy: serviceapi.ProxyHealth{Status: serviceapi.ProxyMissing}},
			},
			want: false,
		},
		{
			name: "mixed - one bad running",
			rows: []serviceapi.ProjectStatus{
				{Name: "a", VMRunning: true, Proxy: serviceapi.ProxyHealth{Status: serviceapi.ProxyOK}},
				{Name: "b", VMRunning: true, Proxy: serviceapi.ProxyHealth{Status: serviceapi.ProxyMissing}},
				{Name: "c", VMRunning: false, Proxy: serviceapi.ProxyHealth{Status: serviceapi.ProxyMissing}},
			},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, anyProjectNeedsReconcile(tc.rows))
		})
	}
}

// startApproveStatusDaemon spins a real serviceapi.Server with the VM
// handlers registered (including GET /vm/approve-state) on
// identity.Prod's socket, scoped to a temp $HOME so it never collides
// with a real devm daemon. tr.Path is "false" so orchestrator.RunStatus's
// tart-list call harmlessly errors instead of shelling out to a real
// tart binary — these tests only exercise the approve-state section of
// `devm status`, so the sandbox itself stays "absent" throughout.
func startApproveStatusDaemon(t *testing.T) func() {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sapi-approve-status-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("HOME", dir)

	_, err = serviceapi.EnsureRuntimeDir(identity.Prod)
	require.NoError(t, err)
	socket := identity.Prod.SocketPath()
	srv := serviceapi.NewServer(socket, serviceapi.Build{Version: "dev"})
	sup := supervisor.New(t.TempDir())
	tr := tart.New()
	tr.Path = "false"
	serviceapi.RegisterVMHandlers(srv, identity.Prod, sup, tr, 0, serviceapi.NewProjectLocks(), nil, serviceapi.NewPopSessionStore(), nil)

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

// TestStatus_ShowsDivergedApproveState verifies `devm status`'s
// approve-gate line reports the daemon's divergence verdict when the
// on-disk devm.yaml no longer matches the last-approved snapshot.
func TestStatus_ShowsDivergedApproveState(t *testing.T) {
	cleanup := startApproveStatusDaemon(t)
	defer cleanup()

	projDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(projDir, "devm.yaml"),
		[]byte("project:\n  name: p\nenv:\n  FOO: new\n"), 0644))

	store := approve.NewStore(identity.Prod)
	require.NoError(t, store.Write("p", []byte("project:\n  name: p\nenv:\n  FOO: old\n"), nil, "user"))

	tr := tart.New()
	tr.Path = "false"
	res, err := orchestrator.RunStatus(identity.Prod, schema.Config{Project: schema.Project{Name: "p"}}, tr, projDir, "")
	require.NoError(t, err)
	require.NotNil(t, res.ApproveState)
	assert.True(t, res.ApproveState.Diverged)

	out := orchestrator.FormatStatusText(res)
	assert.Contains(t, out, "devm.yaml has changed since last approval")
	assert.Contains(t, out, "Review")
}

// TestStatus_ShowsUpToDateApproveState verifies `devm status`'s
// approve-gate line reports "up to date" when the on-disk devm.yaml
// matches the last-approved snapshot exactly.
func TestStatus_ShowsUpToDateApproveState(t *testing.T) {
	cleanup := startApproveStatusDaemon(t)
	defer cleanup()

	contents := []byte("project:\n  name: p\nenv:\n  FOO: same\n")
	projDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(projDir, "devm.yaml"), contents, 0644))

	store := approve.NewStore(identity.Prod)
	require.NoError(t, store.Write("p", contents, nil, "user"))

	tr := tart.New()
	tr.Path = "false"
	res, err := orchestrator.RunStatus(identity.Prod, schema.Config{Project: schema.Project{Name: "p"}}, tr, projDir, "")
	require.NoError(t, err)
	require.NotNil(t, res.ApproveState)
	assert.False(t, res.ApproveState.Diverged)

	out := orchestrator.FormatStatusText(res)
	assert.Contains(t, out, "Approve gate: up to date.")
}

// TestStatus_ShowsNoApproveLineWhenUnsupported verifies `devm status`
// omits the approve-gate line entirely (rather than reporting an
// error) when the daemon 404s /vm/approve-state — the shape an older,
// pre-approve-gate daemon build returns. This is the backward-compat
// path: no error, no line, silent degrade.
func TestStatus_ShowsNoApproveLineWhenUnsupported(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "sapi-approve-404-status-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("HOME", dir)

	_, err = serviceapi.EnsureRuntimeDir(identity.Prod)
	require.NoError(t, err)
	socket := identity.Prod.SocketPath()
	srv := serviceapi.NewServer(socket, serviceapi.Build{Version: "dev"})
	// Deliberately no VM handlers registered — mirrors a daemon build
	// that predates the approve gate, which 404s any /vm/* route.

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

	projDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(projDir, "devm.yaml"),
		[]byte("project:\n  name: p\n"), 0644))

	tr := tart.New()
	tr.Path = "false"
	res, err := orchestrator.RunStatus(identity.Prod, schema.Config{Project: schema.Project{Name: "p"}}, tr, projDir, "")
	require.NoError(t, err)
	assert.Nil(t, res.ApproveState)
	assert.Empty(t, res.ApproveError)

	out := orchestrator.FormatStatusText(res)
	assert.NotContains(t, out, "Approve gate")
}
