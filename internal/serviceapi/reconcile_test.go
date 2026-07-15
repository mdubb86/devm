package serviceapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/mdubb86/devm/internal/reconcile"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/supervisor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createTestCA creates a dummy CA file in the test's HOME/.../ca/ directory.
func createTestCA(t *testing.T) {
	t.Helper()
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	caDir := filepath.Join(home, "Library", "Application Support", "devm", "ca")
	require.NoError(t, os.MkdirAll(caDir, 0o755))
	caCert := filepath.Join(caDir, "root.crt")
	require.NoError(t, os.WriteFile(caCert, []byte("-----BEGIN CERTIFICATE-----\nDUMMY\n-----END CERTIFICATE-----\n"), 0o644))
}

func TestVMReconcile_NoSnapshotYet_TreatsAllAsFullDiff(t *testing.T) {
	// Regression: without a baseline snapshot, ComputeAllChanges was
	// diffing against schema.Config{} — every teardown-bucket kind
	// spuriously surfaced as pending after a fresh cold-start. Fix
	// seeds the snapshot at cold-start; this test locks that the
	// reconcile handler now has a baseline available whenever the VM
	// has ever been started.
	t.Setenv("HOME", t.TempDir())
	cfg := schema.Config{
		Project:  schema.Project{ID: "p", VMName: "p-vm"},
		Packages: []string{"jq"},
	}
	// Simulate what cold-start does: write snapshot.
	require.NoError(t, WriteStateSnapshot("p", StateSnapshot{Cfg: cfg}))

	// Reconcile against unchanged cfg → nothing to do.
	req := VMReconcileRequest{ProjectID: "p", VMName: "p-vm", Cfg: cfg}
	body, _ := json.Marshal(req)

	server := NewServer(SocketPath(), Build{})
	locks := NewProjectLocks()
	RegisterReconcileHandler(server, locks, &fakeApply{}, &fakeTartList{running: true, vmName: "p-vm"}, supervisor.New(""))

	rec := httptest.NewRecorder()
	server.mux.ServeHTTP(rec, httptest.NewRequest("POST", "/vm/reconcile", bytes.NewReader(body)))
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	var resp VMReconcileResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Empty(t, resp.Applied)
	assert.Empty(t, resp.TeardownRequired,
		"unchanged cfg against seeded snapshot must produce zero pending changes")
}

func TestVMReconcile_LiveChangeAppliesAndSnapshots(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	createTestCA(t)

	// Seed baseline snapshot: old_cfg has FOO=old.
	oldCfg := schema.Config{
		Project: schema.Project{ID: "p", VMName: "p-vm"},
		Env:     map[string]schema.EnvValue{"FOO": {Literal: "old"}},
	}
	require.NoError(t, WriteStateSnapshot("p", StateSnapshot{Cfg: oldCfg}))

	// New cfg: FOO=new (bucket=live).
	newCfg := oldCfg
	newCfg.Env = map[string]schema.EnvValue{"FOO": {Literal: "new"}}

	// Fake reconcile deps that record what would run without shelling out.
	// Concrete wiring provided by an interface in the handler; see impl.

	req := VMReconcileRequest{
		ProjectID: "p", VMName: "p-vm", Cfg: newCfg,
		WorkspaceHostPath: "/tmp/repo",
	}
	body, _ := json.Marshal(req)

	server := NewServer(SocketPath(), Build{})
	locks := NewProjectLocks()
	RegisterReconcileHandler(server, locks /* fake apply */, &fakeApply{}, &fakeTartList{running: true, vmName: "p-vm"}, supervisor.New(""))

	rec := httptest.NewRecorder()
	server.mux.ServeHTTP(rec, httptest.NewRequest("POST", "/vm/reconcile", bytes.NewReader(body)))

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	var resp VMReconcileResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp.Applied, "FOO change should apply")
	assert.Empty(t, resp.TeardownRequired)

	// Snapshot persisted.
	got, err := ReadStateSnapshot("p")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "new", got.Cfg.Env["FOO"].Literal)
}

func TestVMReconcile_TeardownRequiredDoesNotPersist(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	oldCfg := schema.Config{
		Project:  schema.Project{ID: "p", VMName: "p-vm"},
		Packages: []string{"jq"},
	}
	require.NoError(t, WriteStateSnapshot("p", StateSnapshot{Cfg: oldCfg}))
	newCfg := oldCfg
	newCfg.Packages = []string{"jq", "yq"} // bucket=recreate

	req := VMReconcileRequest{ProjectID: "p", VMName: "p-vm", Cfg: newCfg}
	body, _ := json.Marshal(req)

	server := NewServer(SocketPath(), Build{})
	locks := NewProjectLocks()
	RegisterReconcileHandler(server, locks, &fakeApply{}, &fakeTartList{running: true, vmName: "p-vm"}, supervisor.New(""))

	rec := httptest.NewRecorder()
	server.mux.ServeHTTP(rec, httptest.NewRequest("POST", "/vm/reconcile", bytes.NewReader(body)))

	require.Equal(t, http.StatusOK, rec.Code)
	var resp VMReconcileResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp.TeardownRequired)

	// Snapshot NOT overwritten with new_cfg (packages change is pending).
	got, err := ReadStateSnapshot("p")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, []string{"jq"}, got.Cfg.Packages, "packages change must not be persisted until user acts")
}

func TestVMReconcile_PerServiceEnvChange_PersistsInSnapshot(t *testing.T) {
	// Per-service env change (as opposed to top-level cfg.Env) must
	// land in the snapshot's Services[svc].Env, not just cfg.Env.
	// Otherwise the same change re-surfaces on every subsequent
	// reconcile.
	t.Setenv("HOME", t.TempDir())
	createTestCA(t)
	oldCfg := schema.Config{
		Project: schema.Project{ID: "p", VMName: "p-vm"},
		Services: map[string]schema.Service{
			"web": {
				Exec: []string{"/bin/true"},
				Env:  map[string]schema.EnvValue{"OLD": {Literal: "a"}},
			},
		},
	}
	require.NoError(t, WriteStateSnapshot("p", StateSnapshot{Cfg: oldCfg}))

	newCfg := oldCfg
	newSvc := oldCfg.Services["web"]
	newSvc.Env = map[string]schema.EnvValue{"OLD": {Literal: "b"}}
	newCfg.Services = map[string]schema.Service{"web": newSvc}

	req := VMReconcileRequest{ProjectID: "p", VMName: "p-vm", Cfg: newCfg}
	body, _ := json.Marshal(req)

	server := NewServer(SocketPath(), Build{})
	locks := NewProjectLocks()
	RegisterReconcileHandler(server, locks, &fakeApply{}, &fakeTartList{running: true, vmName: "p-vm"}, supervisor.New(""))

	rec := httptest.NewRecorder()
	server.mux.ServeHTTP(rec, httptest.NewRequest("POST", "/vm/reconcile", bytes.NewReader(body)))
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	got, err := ReadStateSnapshot("p")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "b", got.Cfg.Services["web"].Env["OLD"].Literal,
		"snapshot must reflect per-service env change; otherwise it re-surfaces every reconcile")
}

func TestVMReconcile_MixedLiveAndTeardownOnSameService_PreservesPending(t *testing.T) {
	// Same service has BOTH a live change (exec) AND a teardown-required
	// change (masks). Applying the live exec must not silently absorb
	// the pending masks change into the snapshot. Next reconcile must
	// still surface the masks change as teardown_required.
	t.Setenv("HOME", t.TempDir())
	createTestCA(t)
	oldCfg := schema.Config{
		Project: schema.Project{ID: "p", VMName: "p-vm"},
		Services: map[string]schema.Service{
			"web": {
				Exec:  []string{"/bin/true"},
				Masks: []schema.Mask{{Path: "data", Size: "10m"}},
			},
		},
	}
	require.NoError(t, WriteStateSnapshot("p", StateSnapshot{Cfg: oldCfg}))

	newCfg := oldCfg
	newSvc := oldCfg.Services["web"]
	newSvc.Exec = []string{"/bin/echo", "hi"} // live change
	newSvc.Masks = []schema.Mask{
		{Path: "data", Size: "10m"},
		{Path: "logs", Size: "5m"}, // teardown-required addition
	}
	newCfg.Services = map[string]schema.Service{"web": newSvc}

	req := VMReconcileRequest{ProjectID: "p", VMName: "p-vm", Cfg: newCfg}
	body, _ := json.Marshal(req)

	server := NewServer(SocketPath(), Build{})
	locks := NewProjectLocks()
	RegisterReconcileHandler(server, locks, &fakeApply{}, &fakeTartList{running: true, vmName: "p-vm"}, supervisor.New(""))

	rec := httptest.NewRecorder()
	server.mux.ServeHTTP(rec, httptest.NewRequest("POST", "/vm/reconcile", bytes.NewReader(body)))
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	got, err := ReadStateSnapshot("p")
	require.NoError(t, err)
	require.NotNil(t, got)
	// Live change (exec) landed in snapshot.
	assert.Equal(t, []string{"/bin/echo", "hi"}, got.Cfg.Services["web"].Exec)
	// Pending masks change did NOT land — old masks preserved so
	// next reconcile still surfaces masks add as teardown_required.
	assert.Equal(t, []schema.Mask{{Path: "data", Size: "10m"}}, got.Cfg.Services["web"].Masks)
}

func TestVMReconcile_SecretDriftEmitsKindSecretChange(t *testing.T) {
	// CLI resolves + hashes secret refs (login-keychain access happens
	// in the user context) and sends the map on every /vm/reconcile
	// call. Compared against the last-applied snapshot's SecretHashes,
	// the diff engine must surface a KindSecretChange when the hash for
	// an existing secret ref rotates.
	t.Setenv("HOME", t.TempDir())

	require.NoError(t, WriteStateSnapshot("p", StateSnapshot{
		Cfg:          schema.Config{Project: schema.Project{ID: "p", VMName: "p-vm"}},
		SecretHashes: map[string]string{"TOK": "old-hash"},
	}))

	req := VMReconcileRequest{
		ProjectID:    "p",
		VMName:       "p-vm",
		Cfg:          schema.Config{Project: schema.Project{ID: "p", VMName: "p-vm"}},
		SecretHashes: map[string]string{"TOK": "new-hash"},
	}
	body, _ := json.Marshal(req)

	server := NewServer(SocketPath(), Build{})
	locks := NewProjectLocks()
	RegisterReconcileHandler(server, locks, &fakeApply{}, &fakeTartList{running: true, vmName: "p-vm"}, supervisor.New(""))

	rec := httptest.NewRecorder()
	server.mux.ServeHTTP(rec, httptest.NewRequest("POST", "/vm/reconcile", bytes.NewReader(body)))
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	var resp VMReconcileResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	kinds := changeKinds(resp.AppliedIronProxy)
	assert.Contains(t, kinds, reconcile.KindSecretChange,
		"rotated secret hash must surface as a KindSecretChange in AppliedIronProxy")
	assert.Empty(t, resp.Applied)
	assert.Empty(t, resp.TeardownRequired)
}

func TestVMReconcile_LiveChangeOnly_PreservesSecretHashes(t *testing.T) {
	// Regression for F1: the live-apply success path wrote a fresh
	// StateSnapshot{Cfg, TemplateContents} without carrying forward
	// SecretHashes from the prior snapshot. That clobbered the baseline
	// to nil, so the very next reconcile treated every existing secret
	// as newly added (KindSecretAdd) — an iron-proxy respawn storm with
	// no actual secret drift. A live-only reconcile (no network, no
	// secret rotation) must leave SecretHashes untouched on disk.
	t.Setenv("HOME", t.TempDir())
	createTestCA(t)

	oldCfg := schema.Config{
		Project: schema.Project{ID: "p", VMName: "p-vm"},
		Env:     map[string]schema.EnvValue{"FOO": {Literal: "old"}},
	}
	require.NoError(t, WriteStateSnapshot("p", StateSnapshot{
		Cfg:          oldCfg,
		SecretHashes: map[string]string{"A": "h1"},
	}))

	// New cfg: FOO=new (bucket=live). Same secret hash — no drift.
	newCfg := oldCfg
	newCfg.Env = map[string]schema.EnvValue{"FOO": {Literal: "new"}}

	req := VMReconcileRequest{
		ProjectID: "p", VMName: "p-vm", Cfg: newCfg,
		WorkspaceHostPath: "/tmp/repo",
		SecretHashes:      map[string]string{"A": "h1"},
	}
	body, _ := json.Marshal(req)

	server := NewServer(SocketPath(), Build{})
	locks := NewProjectLocks()
	RegisterReconcileHandler(server, locks, &fakeApply{}, &fakeTartList{running: true, vmName: "p-vm"}, healthyIronProxySupervisor(t, "p"))

	rec := httptest.NewRecorder()
	server.mux.ServeHTTP(rec, httptest.NewRequest("POST", "/vm/reconcile", bytes.NewReader(body)))
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	var resp VMReconcileResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp.Applied, "FOO change should apply")
	assert.Empty(t, resp.AppliedIronProxy, "no secret drift or network change; nothing for iron-proxy")
	assert.Empty(t, resp.TeardownRequired)

	snap, err := ReadStateSnapshot("p")
	require.NoError(t, err)
	require.NotNil(t, snap)
	assert.Equal(t, map[string]string{"A": "h1"}, snap.SecretHashes,
		"live-only reconcile must not clobber SecretHashes to nil")
}

func TestVMReconcile_NetworkAddSurfacesAsAppliedIronProxy(t *testing.T) {
	// Network-allow additions are BucketIronProxyRestart changes: the
	// daemon does not apply them itself. They must come back to the CLI
	// via AppliedIronProxy (which the CLI dispatches to
	// /vm/apply-iron-proxy), not Applied or TeardownRequired.
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, WriteStateSnapshot("p", StateSnapshot{
		Cfg: schema.Config{
			Project: schema.Project{ID: "p", VMName: "p-vm"},
			Network: schema.Network{Allow: []schema.AllowEntry{{Host: "a.com"}}},
		},
	}))

	req := VMReconcileRequest{
		ProjectID: "p",
		VMName:    "p-vm",
		Cfg: schema.Config{
			Project: schema.Project{ID: "p", VMName: "p-vm"},
			Network: schema.Network{Allow: []schema.AllowEntry{{Host: "a.com"}, {Host: "b.com"}}},
		},
	}
	body, _ := json.Marshal(req)

	server := NewServer(SocketPath(), Build{})
	locks := NewProjectLocks()
	RegisterReconcileHandler(server, locks, &fakeApply{}, &fakeTartList{running: true, vmName: "p-vm"}, healthyIronProxySupervisor(t, "p"))

	rec := httptest.NewRecorder()
	server.mux.ServeHTTP(rec, httptest.NewRequest("POST", "/vm/reconcile", bytes.NewReader(body)))
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	var resp VMReconcileResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.AppliedIronProxy, 1)
	assert.Equal(t, reconcile.KindNetworkAdd, resp.AppliedIronProxy[0].Kind)
	assert.Equal(t, "b.com", resp.AppliedIronProxy[0].Key)
	assert.Empty(t, resp.Applied)
	assert.Empty(t, resp.TeardownRequired)
}

func TestVMReconcile_MissingIronProxy_EmitsKindIronProxyDown(t *testing.T) {
	// Self-heal: even with a completely unchanged config, a running VM
	// whose iron-proxy is missing/stale must surface a synthetic
	// KindIronProxyDown change on AppliedIronProxy so the CLI's existing
	// dispatch to /vm/apply-iron-proxy respawns it.
	t.Setenv("HOME", t.TempDir())
	cfg := schema.Config{Project: schema.Project{ID: "p", VMName: "p-vm"}}
	require.NoError(t, WriteStateSnapshot("p", StateSnapshot{Cfg: cfg}))

	req := VMReconcileRequest{ProjectID: "p", VMName: "p-vm", Cfg: cfg}
	body, _ := json.Marshal(req)

	server := NewServer(SocketPath(), Build{})
	locks := NewProjectLocks()
	// A fresh supervisor with no adopted iron-proxy process reports the
	// proxy as not Present/Running → computeProxyHealth returns MISSING.
	sup := supervisor.New("")
	RegisterReconcileHandler(server, locks, &fakeApply{}, &fakeTartList{running: true, vmName: "p-vm"}, sup)

	rec := httptest.NewRecorder()
	server.mux.ServeHTTP(rec, httptest.NewRequest("POST", "/vm/reconcile", bytes.NewReader(body)))
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	var resp VMReconcileResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	kinds := changeKinds(resp.AppliedIronProxy)
	assert.Contains(t, kinds, reconcile.KindIronProxyDown,
		"missing iron-proxy on a running VM must surface KindIronProxyDown even with an unchanged config")
}

func TestVMReconcile_StoppedVM_MissingIronProxy_DoesNotEmitKindIronProxyDown(t *testing.T) {
	// The heal is gated on the VM actually running — a stopped VM's
	// iron-proxy is expected to be down and must not trigger the heal
	// path (there's nothing to respawn against).
	t.Setenv("HOME", t.TempDir())
	cfg := schema.Config{Project: schema.Project{ID: "p", VMName: "p-vm"}}
	require.NoError(t, WriteStateSnapshot("p", StateSnapshot{Cfg: cfg}))

	req := VMReconcileRequest{ProjectID: "p", VMName: "p-vm", Cfg: cfg}
	body, _ := json.Marshal(req)

	server := NewServer(SocketPath(), Build{})
	locks := NewProjectLocks()
	sup := supervisor.New("")
	RegisterReconcileHandler(server, locks, &fakeApply{}, &fakeTartList{running: false, vmName: "p-vm"}, sup)

	rec := httptest.NewRecorder()
	server.mux.ServeHTTP(rec, httptest.NewRequest("POST", "/vm/reconcile", bytes.NewReader(body)))
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	var resp VMReconcileResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Empty(t, resp.AppliedIronProxy,
		"stopped VM must not trigger the iron-proxy heal path")
}

// healthyIronProxySupervisor returns a *supervisor.Supervisor for which
// computeProxyHealth(sup, projectID) reports ProxyOK: an adopted PID
// that's actually alive (this test process itself, so Status() reports
// Running=true without spawning anything) plus a stub on-disk config
// file (computeProxyHealth only checks that it exists). Tests that
// aren't exercising the Task 4 self-heal path need this so the heal
// doesn't spuriously add a KindIronProxyDown to AppliedIronProxy.
func healthyIronProxySupervisor(t *testing.T, projectID string) *supervisor.Supervisor {
	t.Helper()
	sup := supervisor.New("")
	sup.Adopt(supervisor.Key{ProjectID: projectID, Role: supervisor.RoleProxy}, os.Getpid())
	cfgPath, err := IronProxyConfigPath(projectID)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(cfgPath), 0o755))
	require.NoError(t, os.WriteFile(cfgPath, []byte("stub\n"), 0o600))
	return sup
}

func changeKinds(cs []reconcile.Change) []reconcile.ChangeKind {
	out := make([]reconcile.ChangeKind, len(cs))
	for i, c := range cs {
		out[i] = c.Kind
	}
	return out
}

// fakeApply is a stand-in for the real ApplyLive; it records that it
// was called, captures SSH byte fields, and returns success.
type fakeApply struct {
	called          bool
	lastSSHAuthPub  []byte
	lastSSHHostPriv []byte
	lastSSHHostPub  []byte
}

func (f *fakeApply) ApplyLive(changes []reconcile.Change, cfg schema.Config, repoRoot, vmName string, caPEM, sshAuthPub, sshHostPriv, sshHostPub []byte) error {
	f.called = true
	f.lastSSHAuthPub = sshAuthPub
	f.lastSSHHostPriv = sshHostPriv
	f.lastSSHHostPub = sshHostPub
	return nil
}

// fakeTartList is a stand-in for the daemon's *tart.Tart, reporting a
// fixed running state for one VM name without shelling out to `tart`.
type fakeTartList struct {
	running bool
	vmName  string
}

func (f *fakeTartList) List(ctx context.Context) ([]tart.VM, error) {
	return []tart.VM{{Name: f.vmName, Running: f.running}}, nil
}

func TestVMReconcile_StoppedVM_SkipsApplyAndSnapshot(t *testing.T) {
	// When VM is stopped, /vm/reconcile must not call ApplyLive and
	// must not update the snapshot — changes get picked up at next
	// cold-start's provisioner bundle pipe.
	t.Setenv("HOME", t.TempDir())
	oldCfg := schema.Config{
		Project: schema.Project{ID: "p", VMName: "p-vm"},
		Env:     map[string]schema.EnvValue{"FOO": {Literal: "old"}},
	}
	require.NoError(t, WriteStateSnapshot("p", StateSnapshot{Cfg: oldCfg}))

	newCfg := oldCfg
	newCfg.Env = map[string]schema.EnvValue{"FOO": {Literal: "new"}}

	req := VMReconcileRequest{ProjectID: "p", VMName: "p-vm", Cfg: newCfg}
	body, _ := json.Marshal(req)

	server := NewServer(SocketPath(), Build{})
	locks := NewProjectLocks()
	fake := &fakeApply{}
	// Use a fake tart that reports p-vm as NOT running.
	fakeTart := &fakeTartList{running: false, vmName: "p-vm"}
	RegisterReconcileHandler(server, locks, fake, fakeTart, supervisor.New(""))

	rec := httptest.NewRecorder()
	server.mux.ServeHTTP(rec, httptest.NewRequest("POST", "/vm/reconcile", bytes.NewReader(body)))
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	var resp VMReconcileResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "stopped", resp.SandboxState)
	assert.Empty(t, resp.Applied, "must not apply against stopped VM")
	assert.False(t, fake.called, "ApplyLive must not be called against stopped VM")

	// Snapshot preserved at old cfg — pending change re-surfaces
	// next reconcile (or at cold-start).
	got, err := ReadStateSnapshot("p")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "old", got.Cfg.Env["FOO"].Literal,
		"snapshot must NOT be updated when VM is stopped")
}

func TestVMReconcile_ServiceAddedFromNilServices_NoPanic(t *testing.T) {
	// Regression: when old_cfg.Services was nil (e.g., cold-start with an
	// empty yaml) and new_cfg adds a service with a port, mergeLiveApplied
	// panicked with "assignment to entry in nil map" because its defensive-
	// copy guard skipped when the pre-image Services map was nil. Fixed by
	// dropping the nil check; make() on a nil-length seed and range over
	// nil are both valid.
	t.Setenv("HOME", t.TempDir())
	createTestCA(t)
	oldCfg := schema.Config{
		Project: schema.Project{ID: "p", VMName: "p-vm"},
		// No Services at all — Services is nil.
	}
	require.NoError(t, WriteStateSnapshot("p", StateSnapshot{Cfg: oldCfg}))

	newCfg := oldCfg
	newCfg.Services = map[string]schema.Service{
		"api": {Port: 8080},
	}
	req := VMReconcileRequest{ProjectID: "p", VMName: "p-vm", Cfg: newCfg}
	body, _ := json.Marshal(req)

	server := NewServer(SocketPath(), Build{})
	locks := NewProjectLocks()
	RegisterReconcileHandler(server, locks, &fakeApply{}, &fakeTartList{running: true, vmName: "p-vm"}, supervisor.New(""))

	rec := httptest.NewRecorder()
	server.mux.ServeHTTP(rec, httptest.NewRequest("POST", "/vm/reconcile", bytes.NewReader(body)))
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	// Snapshot must reflect the new port in the newly-created Services map.
	got, err := ReadStateSnapshot("p")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.Cfg.Services, "Services should be initialized after live apply, not nil")
	assert.Equal(t, 8080, got.Cfg.Services["api"].Port)
}

func TestVMReconcile_ForwardsSSHBytesToApplyLive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	createTestCA(t)

	// Seed a snapshot so the reconcile handler treats this as a live-apply candidate.
	oldCfg := schema.Config{
		Project: schema.Project{ID: "p", VMName: "p-vm"},
		Env:     map[string]schema.EnvValue{"FOO": {Literal: "old"}},
	}
	require.NoError(t, WriteStateSnapshot("p", StateSnapshot{Cfg: oldCfg}))

	newCfg := oldCfg
	newCfg.Env = map[string]schema.EnvValue{"FOO": {Literal: "new"}}

	req := VMReconcileRequest{
		ProjectID:           "p",
		VMName:              "p-vm",
		Cfg:                 newCfg,
		WorkspaceHostPath:   "/tmp/repo",
		SSHAuthorizedPubkey: []byte("ssh-ed25519 AAAA_pub_marker\n"),
		SSHHostPriv:         []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nHOST_MARKER\n"),
		SSHHostPub:          []byte("ssh-ed25519 AAAA_host_pub_marker\n"),
	}
	body, _ := json.Marshal(req)

	server := NewServer(SocketPath(), Build{})
	locks := NewProjectLocks()
	fake := &fakeApply{}
	RegisterReconcileHandler(server, locks, fake, &fakeTartList{running: true, vmName: "p-vm"}, supervisor.New(""))

	rec := httptest.NewRecorder()
	server.mux.ServeHTTP(rec, httptest.NewRequest("POST", "/vm/reconcile", bytes.NewReader(body)))
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	assert.True(t, fake.called, "ApplyLive must have been invoked for a live env change")
	assert.Equal(t, req.SSHAuthorizedPubkey, fake.lastSSHAuthPub,
		"SSH authorized pubkey must forward to ApplyLive")
	assert.Equal(t, req.SSHHostPriv, fake.lastSSHHostPriv,
		"SSH host privkey must forward to ApplyLive")
	assert.Equal(t, req.SSHHostPub, fake.lastSSHHostPub,
		"SSH host pubkey must forward to ApplyLive")
}
