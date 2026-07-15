package orchestrator

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mdubb86/devm/internal/reconcile"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/mdubb86/devm/internal/supervisor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func reconcileMinimalCfg() schema.Config {
	return schema.Config{
		Project: schema.Project{ID: "x", VMName: "x"},
	}
}

// nopApply is a stand-in for serviceapi.ApplyLiver that records nothing
// and always succeeds — the fake daemon in these tests never actually
// needs to shell into a VM since the live changes under test (env)
// don't require verifying guest-side effects.
type nopApply struct{}

func (nopApply) ApplyLive(changes []reconcile.Change, cfg schema.Config, repoRoot, vmName string, caPEM, sshAuthPub, sshHostPriv, sshHostPub []byte) error {
	return nil
}

// fakeTartList is a stand-in for the daemon's *tart.Tart, reporting a
// fixed running state for one VM name without shelling out to `tart`.
// These orchestrator-level tests exercise the "VM is running" path;
// the stopped-VM path is pinned by serviceapi's own reconcile tests.
type fakeTartList struct {
	running bool
	vmName  string
}

func (f *fakeTartList) List(ctx context.Context) ([]tart.VM, error) {
	return []tart.VM{{Name: f.vmName, Running: f.running}}, nil
}

// startReconcileDaemon spins up a real serviceapi.Server with the
// /vm/reconcile handler registered on a temp Unix socket, and points
// HOME at a temp dir so serviceapi.SocketPath() (and therefore
// serviceapi.NewClient(), which RunReconcile calls internally) resolves
// to it. Returns a cleanup func.
func startReconcileDaemon(t *testing.T) func() {
	t.Helper()
	// Unix domain socket paths are capped at ~104 bytes on macOS/BSD;
	// t.TempDir() nests under /var/folders/.../T/<TestName>/001, which
	// blows that budget once "Library/Application Support/devm/devm.sock"
	// is appended. Use a short /tmp-rooted HOME instead (same trick as
	// serviceapi's own socket-based tests).
	home, err := os.MkdirTemp("/tmp", "devm-home-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv("HOME", home)

	// Create a dummy CA file so reconcile.ApplyLive can load it.
	caDir := filepath.Join(home, "Library", "Application Support", "devm", "ca")
	require.NoError(t, os.MkdirAll(caDir, 0o755))
	caCert := filepath.Join(caDir, "root.crt")
	require.NoError(t, os.WriteFile(caCert, []byte("-----BEGIN CERTIFICATE-----\nDUMMY\n-----END CERTIFICATE-----\n"), 0o644))

	_, err = serviceapi.EnsureRuntimeDir()
	require.NoError(t, err)
	socket := serviceapi.SocketPath()

	sup := healthyIronProxySupervisor(t, "x")
	srv := serviceapi.NewServer(socket, serviceapi.Build{Version: "test"})
	serviceapi.RegisterReconcileHandler(srv, serviceapi.NewProjectLocks(), nopApply{}, &fakeTartList{running: true, vmName: "x"}, sup)

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

// healthyIronProxySupervisor returns a *supervisor.Supervisor that
// reports projectID's iron-proxy as healthy (computeProxyHealth ==
// ProxyOK): an adopted PID that's actually alive (this test process
// itself, so Status() reports Running=true without spawning anything)
// plus a stub on-disk config file (computeProxyHealth only checks that
// it exists). Task 4's reconcile self-heal fires whenever the iron-proxy
// is NOT OK; tests that aren't exercising that heal path need a
// healthy baseline so it stays out of their way.
func healthyIronProxySupervisor(t *testing.T, projectID string) *supervisor.Supervisor {
	t.Helper()
	sup := supervisor.New("")
	sup.Adopt(supervisor.Key{ProjectID: projectID, Role: supervisor.RoleProxy}, os.Getpid())
	cfgPath, err := serviceapi.IronProxyConfigPath(projectID)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(cfgPath), 0o755))
	require.NoError(t, os.WriteFile(cfgPath, []byte("stub\n"), 0o600))
	return sup
}

// fakeTartForSessions returns a *tart.Tart whose `exec` calls always
// exit non-zero, so probeSessions (called only on the teardown-required
// path) resolves to nil sessions without shelling out for real.
func fakeTartForSessions(t *testing.T) *tart.Tart {
	t.Helper()
	tr := tart.New()
	tr.Path = "false"
	return tr
}

func TestRunReconcile_LiveChangeApplies(t *testing.T) {
	cleanup := startReconcileDaemon(t)
	defer cleanup()

	oldCfg := reconcileMinimalCfg()
	oldCfg.Env = map[string]schema.EnvValue{"FOO": {Literal: "old"}}
	require.NoError(t, serviceapi.WriteStateSnapshot("x", serviceapi.StateSnapshot{Cfg: oldCfg}))

	newCfg := reconcileMinimalCfg()
	newCfg.Env = map[string]schema.EnvValue{"FOO": {Literal: "new"}}

	rc, res, err := RunReconcile(newCfg, fakeTartForSessions(t), "/tmp/fake-repo-root", ReconcileOptions{})
	require.NoError(t, err)
	assert.Equal(t, 0, rc)
	assert.Equal(t, "applied", res.NextAction)
	require.Len(t, res.Applied, 1)
	assert.Equal(t, reconcile.KindEnvChange, res.Applied[0].Kind)
	assert.Empty(t, res.RecreateRequired)
}

func TestRunReconcile_IdenticalBaseline_NothingToDo(t *testing.T) {
	cleanup := startReconcileDaemon(t)
	defer cleanup()

	cfg := reconcileMinimalCfg()
	require.NoError(t, serviceapi.WriteStateSnapshot("x", serviceapi.StateSnapshot{Cfg: cfg}))

	rc, res, err := RunReconcile(cfg, fakeTartForSessions(t), "/tmp/fake-repo-root", ReconcileOptions{})
	require.NoError(t, err)
	assert.Equal(t, 0, rc)
	assert.Equal(t, "nothing_to_do", res.NextAction)
	assert.Empty(t, res.Applied)
	assert.Empty(t, res.RecreateRequired)
}

func TestRunReconcile_TeardownRequired_ClassifiesFlavorAndSessions(t *testing.T) {
	cleanup := startReconcileDaemon(t)
	defer cleanup()

	oldCfg := reconcileMinimalCfg()
	oldCfg.Packages = []string{"jq"}
	require.NoError(t, serviceapi.WriteStateSnapshot("x", serviceapi.StateSnapshot{Cfg: oldCfg}))

	newCfg := reconcileMinimalCfg()
	newCfg.Packages = []string{"jq", "yq"}

	rc, res, err := RunReconcile(newCfg, fakeTartForSessions(t), "/tmp/fake-repo-root", ReconcileOptions{})
	require.NoError(t, err)
	assert.Equal(t, 0, rc)
	assert.Equal(t, "needs_approval", res.NextAction)
	require.Len(t, res.RecreateRequired, 1)
	assert.Equal(t, reconcile.KindPackagesChange, res.RecreateRequired[0].Kind)
	assert.Equal(t, reconcile.FlavorTeardownShell, res.Flavor)
	assert.Empty(t, res.Applied)
	// probeSessions is best-effort against a fake tart that always
	// exits non-zero — nil, not an error.
	assert.Nil(t, res.Sessions)
}

func TestRunReconcile_DaemonUnreachable_ReturnsError(t *testing.T) {
	// HOME points at a fresh tmpdir with no daemon listening — the
	// client's request must fail cleanly rather than hang or panic.
	t.Setenv("HOME", t.TempDir())

	cfg := reconcileMinimalCfg()
	rc, res, err := RunReconcile(cfg, fakeTartForSessions(t), "/tmp/fake-repo-root", ReconcileOptions{})
	require.Error(t, err)
	assert.Equal(t, -1, rc)
	assert.Equal(t, ReconcileResult{}, res)
}

// startReconcileDaemonWithIronProxyCapture is a variant of
// startReconcileDaemon that also registers a fake /vm/apply-iron-proxy
// handler recording the Allowlist it was sent, instead of the real
// RegisterApplyIronProxyHandler (which requires an on-disk iron-proxy
// config file + a live spawn). Returns the cleanup func and a pointer
// to the captured request, populated once RunReconcile dispatches
// ApplyIronProxy.
func startReconcileDaemonWithIronProxyCapture(t *testing.T, running bool) (cleanup func(), captured *serviceapi.VMApplyIronProxyRequest) {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "devm-home-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv("HOME", home)

	caDir := filepath.Join(home, "Library", "Application Support", "devm", "ca")
	require.NoError(t, os.MkdirAll(caDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(caDir, "root.crt"),
		[]byte("-----BEGIN CERTIFICATE-----\nDUMMY\n-----END CERTIFICATE-----\n"), 0o644))

	_, err = serviceapi.EnsureRuntimeDir()
	require.NoError(t, err)
	socket := serviceapi.SocketPath()

	srv := serviceapi.NewServer(socket, serviceapi.Build{Version: "test"})
	serviceapi.RegisterReconcileHandler(srv, serviceapi.NewProjectLocks(), nopApply{}, &fakeTartList{running: running, vmName: "x"}, supervisor.New(""))

	req := &serviceapi.VMApplyIronProxyRequest{}
	srv.Register("/vm/apply-iron-proxy", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(req)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(serviceapi.VMApplyIronProxyResponse{Applied: true, VMRunning: running})
	})

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

	return func() { cancel(); <-errCh }, req
}

func TestRunReconcile_DockerTrueCfg_AllowlistIncludesDockerHubHost(t *testing.T) {
	// Regression: the reconcile heal path used to build the
	// apply-iron-proxy allowlist from cfg.Network.Domains() directly,
	// which drops Docker Hub hosts (unlike cold-start's
	// docker.EffectiveAllowlist). A docker:true project healed via
	// reconcile must still get its registry hosts, or `docker pull`
	// breaks post-heal.
	cleanup, captured := startReconcileDaemonWithIronProxyCapture(t, true)
	defer cleanup()

	oldCfg := reconcileMinimalCfg()
	oldCfg.Docker = true
	oldCfg.Network = schema.Network{Allow: []schema.AllowEntry{{Host: "a.com"}}}
	require.NoError(t, serviceapi.WriteStateSnapshot("x", serviceapi.StateSnapshot{Cfg: oldCfg}))

	newCfg := oldCfg
	newCfg.Network = schema.Network{Allow: []schema.AllowEntry{{Host: "a.com"}, {Host: "b.com"}}}

	rc, res, err := RunReconcile(newCfg, fakeTartForSessions(t), "/tmp/fake-repo-root", ReconcileOptions{})
	require.NoError(t, err)
	assert.Equal(t, 0, rc)
	require.NotEmpty(t, res.AppliedIronProxy, "network add is a BucketIronProxyRestart change")

	assert.Contains(t, captured.Allowlist, "registry-1.docker.io",
		"docker:true reconcile heal must include Docker Hub hosts in the apply-iron-proxy allowlist")
	assert.Contains(t, captured.Allowlist, "a.com")
	assert.Contains(t, captured.Allowlist, "b.com")
}
