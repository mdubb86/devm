package orchestrator

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mdubb86/devm/internal/identity"
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
		Project: schema.Project{Name: "x"},
	}
}

// nopApply is a stand-in for serviceapi.ApplyLiver that records nothing
// and always succeeds — the fake daemon in these tests never actually
// needs to shell into a VM since the live changes under test (env)
// don't require verifying guest-side effects.
type nopApply struct{}

func (nopApply) ApplyLive(changes []reconcile.Change, cfg schema.Config, repoRoot, daemonRuntimeDir, vmName string, caPEM, sshAuthPub, sshHostPriv, sshHostPub []byte, identCfg identity.Config, ironProxyURL string) error {
	return nil
}

// nopPackages is a stand-in for serviceapi.PackagesApplier — these
// orchestrator-level tests never exercise a package change, so it only
// needs to satisfy the interface.
type nopPackages struct{}

func (nopPackages) ApplyPackages(ctx context.Context, projectID string, snapCfg schema.Config, macCwd string, adds, removes []string) error {
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
// HOME at a temp dir so identity.Prod.SocketPath() (and therefore
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

	_, err = serviceapi.EnsureRuntimeDir(identity.Prod)
	require.NoError(t, err)
	socket := identity.Prod.SocketPath()

	// A running project's live-apply path pushes its ingress expose map
	// (serviceapi.pushExposeMap), which now fails loud instead of
	// silently no-opping when the project's softnet control socket was
	// never registered (the adopt-in-place fix this test seam exists
	// for). These orchestrator-level tests never spawn a real /vm/start
	// or softnet process, so stand in with a fake listener registered
	// directly via the test-only seam.
	registerFakeSoftnetForOrchestratorTest(t, "x")

	sup := healthyIronProxySupervisor(t, "x")
	srv := serviceapi.NewServer(socket, serviceapi.Build{Version: "test"})
	serviceapi.RegisterReconcileHandler(srv, identity.Prod, serviceapi.NewProjectLocks(), nopApply{}, nopPackages{}, &fakeTartList{running: true, vmName: "x"}, sup, nil, 0)

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

// registerFakeSoftnetForOrchestratorTest spins up a fake softnet
// control-socket listener for projectID and registers it via
// serviceapi.SetSoftnetControlSockForTest — the daemon's real
// registration paths (/vm/start, /vm/apply-iron-proxy, discoverSoftnet)
// never run in these orchestrator-level tests, which drive a bare
// serviceapi.Server directly. The listener accepts and discards every
// line; these tests exercise reconcile's control flow, not the
// expose-map wire shape.
func registerFakeSoftnetForOrchestratorTest(t *testing.T, projectID string) {
	t.Helper()
	// AF_UNIX sun_path is capped at 104 bytes on Darwin; root directly
	// under /tmp with a short name rather than t.TempDir() (same
	// reasoning as startReconcileDaemon's HOME handling above).
	dir, err := os.MkdirTemp("/tmp", "devm-sn-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "c.sock")
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				line, err := bufio.NewReader(c).ReadString('\n')
				if err != nil {
					return
				}
				// setExposeMap is acked; other ops are fire-and-forget.
				if strings.Contains(line, `"op":"setExposeMap"`) {
					_, _ = c.Write([]byte(`{"ok":true,"results":[]}` + "\n"))
				}
			}(c)
		}
	}()
	serviceapi.SetSoftnetControlSockForTest(projectID, sock)
	t.Cleanup(func() { serviceapi.SetSoftnetControlSockForTest(projectID, "") })
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
	sup := supervisor.New(t.TempDir())
	sup.Adopt(supervisor.Key{ProjectID: projectID, Role: supervisor.RoleProxy}, os.Getpid())
	cfgPath, err := serviceapi.IronProxyConfigPath(identity.Prod, projectID)
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
	require.NoError(t, serviceapi.WriteStateSnapshot(identity.Prod, "x", serviceapi.StateSnapshot{Cfg: oldCfg}))

	newCfg := reconcileMinimalCfg()
	newCfg.Env = map[string]schema.EnvValue{"FOO": {Literal: "new"}}

	rc, res, err := RunReconcile(identity.Prod, newCfg, fakeTartForSessions(t), "/tmp/fake-repo-root", ReconcileOptions{})
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
	require.NoError(t, serviceapi.WriteStateSnapshot(identity.Prod, "x", serviceapi.StateSnapshot{Cfg: cfg}))

	rc, res, err := RunReconcile(identity.Prod, cfg, fakeTartForSessions(t), "/tmp/fake-repo-root", ReconcileOptions{})
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
	oldCfg.Install = []string{"true"}
	require.NoError(t, serviceapi.WriteStateSnapshot(identity.Prod, "x", serviceapi.StateSnapshot{Cfg: oldCfg}))

	newCfg := reconcileMinimalCfg()
	newCfg.Install = []string{"true", "false"}

	rc, res, err := RunReconcile(identity.Prod, newCfg, fakeTartForSessions(t), "/tmp/fake-repo-root", ReconcileOptions{})
	require.NoError(t, err)
	assert.Equal(t, 0, rc)
	assert.Equal(t, "needs_approval", res.NextAction)
	require.Len(t, res.RecreateRequired, 1)
	assert.Equal(t, reconcile.KindInstallChange, res.RecreateRequired[0].Kind)
	assert.Equal(t, reconcile.FlavorTeardownVM, res.Flavor)
	assert.Empty(t, res.Applied)
	// probeSessions is best-effort against a fake tart that always
	// exits non-zero — nil, not an error.
	assert.Nil(t, res.Sessions)
}

// TestRunReconcile_PackagesChange_ClassifiesLive proves the `packages:`
// live-bucket flip: a package add/remove now surfaces in Applied (not
// RecreateRequired) and the flavor stays live-only, since apt is
// idempotent and can converge on a running VM without a teardown.
func TestRunReconcile_PackagesChange_ClassifiesLive(t *testing.T) {
	cleanup := startReconcileDaemon(t)
	defer cleanup()

	oldCfg := reconcileMinimalCfg()
	oldCfg.Packages = []string{"jq"}
	require.NoError(t, serviceapi.WriteStateSnapshot(identity.Prod, "x", serviceapi.StateSnapshot{Cfg: oldCfg}))

	newCfg := reconcileMinimalCfg()
	newCfg.Packages = []string{"jq", "yq"}

	rc, res, err := RunReconcile(identity.Prod, newCfg, fakeTartForSessions(t), "/tmp/fake-repo-root", ReconcileOptions{})
	require.NoError(t, err)
	assert.Equal(t, 0, rc)
	assert.Equal(t, "applied", res.NextAction)
	require.Len(t, res.Applied, 1)
	assert.Equal(t, reconcile.KindPackageAdd, res.Applied[0].Kind)
	assert.Equal(t, "yq", res.Applied[0].Key)
	assert.Equal(t, reconcile.FlavorLiveOnly, res.Flavor)
	assert.Empty(t, res.RecreateRequired)
}

func TestRunReconcile_DaemonUnreachable_ReturnsError(t *testing.T) {
	// HOME points at a fresh tmpdir with no daemon listening — the
	// client's request must fail cleanly rather than hang or panic.
	t.Setenv("HOME", t.TempDir())

	cfg := reconcileMinimalCfg()
	rc, res, err := RunReconcile(identity.Prod, cfg, fakeTartForSessions(t), "/tmp/fake-repo-root", ReconcileOptions{})
	require.Error(t, err)
	assert.Equal(t, -1, rc)
	assert.Equal(t, ReconcileResult{}, res)
}

// TestRunReconcile_ApproveRequired_SurfacesMessageVerbatim proves the
// real production path — cmd/devm/reconcile.go's RunE calls exactly
// this function — surfaces the daemon's clean multi-line
// approve_required refusal, not a JSON-wrapped error body. No
// approve.Store snapshot is written for "x", so isApproveDiverged
// treats the project as diverged (no snapshot at all) and the daemon
// refuses with 409 before RunReconcile ever reaches the live-apply or
// teardown-classification logic.
func TestRunReconcile_ApproveRequired_SurfacesMessageVerbatim(t *testing.T) {
	cleanup := startReconcileDaemon(t)
	defer cleanup()

	cfg := reconcileMinimalCfg()
	require.NoError(t, serviceapi.WriteStateSnapshot(identity.Prod, "x", serviceapi.StateSnapshot{Cfg: cfg}))

	repoRoot := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, "devm.yaml"), []byte("project:\n  name: x\n"), 0o644))

	rc, res, err := RunReconcile(identity.Prod, cfg, fakeTartForSessions(t), repoRoot, ReconcileOptions{})
	require.Error(t, err)
	assert.Equal(t, -1, rc)
	assert.Equal(t, ReconcileResult{}, res)
	assert.Contains(t, err.Error(), "devm.yaml (or devm.me.yaml) has changed since it was last approved.")
	assert.Contains(t, err.Error(), "Run `devm approve`")
	assert.NotContains(t, err.Error(), `"code"`, "error must be the daemon's clean message, not the raw JSON body")
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

	_, err = serviceapi.EnsureRuntimeDir(identity.Prod)
	require.NoError(t, err)
	socket := identity.Prod.SocketPath()

	srv := serviceapi.NewServer(socket, serviceapi.Build{Version: "test"})
	serviceapi.RegisterReconcileHandler(srv, identity.Prod, serviceapi.NewProjectLocks(), nopApply{}, nopPackages{}, &fakeTartList{running: running, vmName: "x"}, supervisor.New(t.TempDir()), nil, 0)

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

