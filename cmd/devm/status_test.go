package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mdubb86/devm/internal/identity"
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
	t.Setenv("DEVM_RUNTIME_DIR", dir)

	srv := serviceapi.NewServer(serviceapi.SocketPath(identity.Prod), serviceapi.Build{Version: "dev"})
	sup := supervisor.New("")
	serviceapi.RegisterStatusAllHandler(srv, identity.Prod, sup, &fakeStatusTart{running: running})

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
