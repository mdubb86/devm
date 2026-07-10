package serviceapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mdubb86/devm/internal/reconcile"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVMReconcile_LiveChangeAppliesAndSnapshots(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Seed baseline snapshot: old_cfg has FOO=old.
	oldCfg := schema.Config{
		Project: schema.Project{ID: "p", VMName: "p-vm"},
		Env:     map[string]schema.EnvValue{"FOO": {Literal: "old"}},
	}
	require.NoError(t, WriteStateCfg("p", oldCfg))

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
	RegisterReconcileHandler(server, locks, /* fake apply */ &fakeApply{}, &fakeTartList{running: true, vmName: "p-vm"})

	rec := httptest.NewRecorder()
	server.mux.ServeHTTP(rec, httptest.NewRequest("POST", "/vm/reconcile", bytes.NewReader(body)))

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	var resp VMReconcileResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp.Applied, "FOO change should apply")
	assert.Empty(t, resp.TeardownRequired)

	// Snapshot persisted.
	got, err := ReadStateCfg("p")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "new", got.Env["FOO"].Literal)
}

func TestVMReconcile_TeardownRequiredDoesNotPersist(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	oldCfg := schema.Config{
		Project:  schema.Project{ID: "p", VMName: "p-vm"},
		Packages: []string{"jq"},
	}
	require.NoError(t, WriteStateCfg("p", oldCfg))
	newCfg := oldCfg
	newCfg.Packages = []string{"jq", "yq"} // bucket=recreate

	req := VMReconcileRequest{ProjectID: "p", VMName: "p-vm", Cfg: newCfg}
	body, _ := json.Marshal(req)

	server := NewServer(SocketPath(), Build{})
	locks := NewProjectLocks()
	RegisterReconcileHandler(server, locks, &fakeApply{}, &fakeTartList{running: true, vmName: "p-vm"})

	rec := httptest.NewRecorder()
	server.mux.ServeHTTP(rec, httptest.NewRequest("POST", "/vm/reconcile", bytes.NewReader(body)))

	require.Equal(t, http.StatusOK, rec.Code)
	var resp VMReconcileResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp.TeardownRequired)

	// Snapshot NOT overwritten with new_cfg (packages change is pending).
	got, err := ReadStateCfg("p")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, []string{"jq"}, got.Packages, "packages change must not be persisted until user acts")
}

func TestVMReconcile_PerServiceEnvChange_PersistsInSnapshot(t *testing.T) {
	// Per-service env change (as opposed to top-level cfg.Env) must
	// land in the snapshot's Services[svc].Env, not just cfg.Env.
	// Otherwise the same change re-surfaces on every subsequent
	// reconcile.
	t.Setenv("HOME", t.TempDir())
	oldCfg := schema.Config{
		Project: schema.Project{ID: "p", VMName: "p-vm"},
		Services: map[string]schema.Service{
			"web": {
				Exec: []string{"/bin/true"},
				Env:  map[string]schema.EnvValue{"OLD": {Literal: "a"}},
			},
		},
	}
	require.NoError(t, WriteStateCfg("p", oldCfg))

	newCfg := oldCfg
	newSvc := oldCfg.Services["web"]
	newSvc.Env = map[string]schema.EnvValue{"OLD": {Literal: "b"}}
	newCfg.Services = map[string]schema.Service{"web": newSvc}

	req := VMReconcileRequest{ProjectID: "p", VMName: "p-vm", Cfg: newCfg}
	body, _ := json.Marshal(req)

	server := NewServer(SocketPath(), Build{})
	locks := NewProjectLocks()
	RegisterReconcileHandler(server, locks, &fakeApply{}, &fakeTartList{running: true, vmName: "p-vm"})

	rec := httptest.NewRecorder()
	server.mux.ServeHTTP(rec, httptest.NewRequest("POST", "/vm/reconcile", bytes.NewReader(body)))
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	got, err := ReadStateCfg("p")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "b", got.Services["web"].Env["OLD"].Literal,
		"snapshot must reflect per-service env change; otherwise it re-surfaces every reconcile")
}

func TestVMReconcile_MixedLiveAndTeardownOnSameService_PreservesPending(t *testing.T) {
	// Same service has BOTH a live change (exec) AND a teardown-required
	// change (masks). Applying the live exec must not silently absorb
	// the pending masks change into the snapshot. Next reconcile must
	// still surface the masks change as teardown_required.
	t.Setenv("HOME", t.TempDir())
	oldCfg := schema.Config{
		Project: schema.Project{ID: "p", VMName: "p-vm"},
		Services: map[string]schema.Service{
			"web": {
				Exec:  []string{"/bin/true"},
				Masks: []schema.Mask{{Path: "data", Size: "10m"}},
			},
		},
	}
	require.NoError(t, WriteStateCfg("p", oldCfg))

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
	RegisterReconcileHandler(server, locks, &fakeApply{}, &fakeTartList{running: true, vmName: "p-vm"})

	rec := httptest.NewRecorder()
	server.mux.ServeHTTP(rec, httptest.NewRequest("POST", "/vm/reconcile", bytes.NewReader(body)))
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	got, err := ReadStateCfg("p")
	require.NoError(t, err)
	require.NotNil(t, got)
	// Live change (exec) landed in snapshot.
	assert.Equal(t, []string{"/bin/echo", "hi"}, got.Services["web"].Exec)
	// Pending masks change did NOT land — old masks preserved so
	// next reconcile still surfaces masks add as teardown_required.
	assert.Equal(t, []schema.Mask{{Path: "data", Size: "10m"}}, got.Services["web"].Masks)
}

// fakeApply is a stand-in for the real ApplyLive; it records that it
// was called and returns success.
type fakeApply struct{ called bool }

func (f *fakeApply) ApplyLive(changes []reconcile.Change, cfg schema.Config, repoRoot, vmName string) error {
	f.called = true
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
	require.NoError(t, WriteStateCfg("p", oldCfg))

	newCfg := oldCfg
	newCfg.Env = map[string]schema.EnvValue{"FOO": {Literal: "new"}}

	req := VMReconcileRequest{ProjectID: "p", VMName: "p-vm", Cfg: newCfg}
	body, _ := json.Marshal(req)

	server := NewServer(SocketPath(), Build{})
	locks := NewProjectLocks()
	fake := &fakeApply{}
	// Use a fake tart that reports p-vm as NOT running.
	fakeTart := &fakeTartList{running: false, vmName: "p-vm"}
	RegisterReconcileHandler(server, locks, fake, fakeTart)

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
	got, err := ReadStateCfg("p")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "old", got.Env["FOO"].Literal,
		"snapshot must NOT be updated when VM is stopped")
}
