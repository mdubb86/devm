package serviceapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mdubb86/devm/internal/reconcile"
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
	RegisterReconcileHandler(server, locks, /* fake apply */ &fakeApply{})

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
	RegisterReconcileHandler(server, locks, &fakeApply{})

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

// fakeApply is a stand-in for the real ApplyLive; it records that it
// was called and returns success.
type fakeApply struct{ called bool }

func (f *fakeApply) ApplyLive(changes []reconcile.Change, cfg schema.Config, repoRoot, vmName string) error {
	f.called = true
	return nil
}
