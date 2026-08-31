// internal/serviceapi/approve_test.go
package serviceapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdubb86/devm/internal/approve"
	"github.com/mdubb86/devm/internal/identity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// approveTestSetup writes a project's devm.yaml (+ optional
// devm.me.yaml) into a tempdir, sets HOME to a separate tempdir, and
// returns (identityCfg, projectDir, snapshotStore).
func approveTestSetup(t *testing.T, devm, me string) (identity.Config, string, *approve.Store) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	projDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(projDir, "devm.yaml"), []byte(devm), 0644))
	if me != "" {
		require.NoError(t, os.WriteFile(filepath.Join(projDir, "devm.me.yaml"), []byte(me), 0644))
	}
	cfg := identity.Config{Name: "devm-test"}
	return cfg, projDir, approve.NewStore(cfg)
}

func TestApproveState_NoSnapshotReportsDiverged(t *testing.T) {
	cfg, projDir, _ := approveTestSetup(t, "project:\n  name: p\n", "")
	req := httptest.NewRequest(http.MethodGet, "/vm/approve-state?project=proj-1&mac_cwd="+projDir, nil)
	rr := httptest.NewRecorder()
	handleApproveState(cfg).ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["diverged"], "no snapshot → diverged=true (first-run bootstrap territory)")
	assert.Equal(t, "absent", resp["approved_devm_sha"])
	assert.Nil(t, resp["approved_since"])
}

func TestApproveState_SnapshotEqualReportsNotDiverged(t *testing.T) {
	cfg, projDir, store := approveTestSetup(t, "project:\n  name: p\n", "env:\n  X: 1\n")
	require.NoError(t, store.Write("proj-1", []byte("project:\n  name: p\n"), []byte("env:\n  X: 1\n"), "user"))
	req := httptest.NewRequest(http.MethodGet, "/vm/approve-state?project=proj-1&mac_cwd="+projDir, nil)
	rr := httptest.NewRecorder()
	handleApproveState(cfg).ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, false, resp["diverged"])
	assert.NotNil(t, resp["approved_since"])
}

func TestApproveState_ChangedByteReportsDiverged(t *testing.T) {
	cfg, projDir, store := approveTestSetup(t, "project:\n  name: p2\n", "")
	require.NoError(t, store.Write("proj-1", []byte("project:\n  name: p\n"), nil, "user"))
	req := httptest.NewRequest(http.MethodGet, "/vm/approve-state?project=proj-1&mac_cwd="+projDir, nil)
	rr := httptest.NewRecorder()
	handleApproveState(cfg).ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["diverged"])
}

func TestApproveState_RequiresProjectAndMacCwd(t *testing.T) {
	cfg := identity.Config{Name: "devm-test"}
	for _, url := range []string{
		"/vm/approve-state",
		"/vm/approve-state?project=x",
		"/vm/approve-state?mac_cwd=/tmp/y",
	} {
		rr := httptest.NewRecorder()
		handleApproveState(cfg).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, url, nil))
		require.Equal(t, http.StatusBadRequest, rr.Code, "url=%s", url)
		assert.True(t, strings.Contains(rr.Body.String(), "project") || strings.Contains(rr.Body.String(), "mac_cwd"))
	}
}

func TestApprove_AdvancesSnapshotToCurrentBytes(t *testing.T) {
	cfg, projDir, store := approveTestSetup(t, "project:\n  name: p\n", "env:\n  X: 1\n")
	req := httptest.NewRequest(http.MethodPost, "/vm/approve?project=proj-1&mac_cwd="+projDir, nil)
	rr := httptest.NewRecorder()
	handleApprove(cfg).ServeHTTP(rr, req)
	require.Equal(t, http.StatusNoContent, rr.Code)
	snap, ok, err := store.Read("proj-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "project:\n  name: p\n", string(snap.DevmYAML))
	assert.Equal(t, "env:\n  X: 1\n", string(snap.MeYAML))
	assert.Equal(t, "user", snap.Manifest.Source)
}

func TestApprove_IdempotentOnAlreadyApproved(t *testing.T) {
	cfg, projDir, store := approveTestSetup(t, "project:\n  name: p\n", "")
	require.NoError(t, store.Write("proj-1", []byte("project:\n  name: p\n"), nil, "user"))
	req := httptest.NewRequest(http.MethodPost, "/vm/approve?project=proj-1&mac_cwd="+projDir, nil)
	rr := httptest.NewRecorder()
	handleApprove(cfg).ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNoContent, rr.Code)
}

func TestApprove_RemovesStaleMeYAMLWhenAbsentOnMac(t *testing.T) {
	cfg, projDir, store := approveTestSetup(t, "project:\n  name: p\n", "")
	// Prior snapshot has a me.yaml.
	require.NoError(t, store.Write("proj-1", []byte("project:\n  name: p\n"), []byte("env:\n  OLD: 1\n"), "user"))
	// Mac side does not have me.yaml. Approve must remove the old copy from the snapshot.
	req := httptest.NewRequest(http.MethodPost, "/vm/approve?project=proj-1&mac_cwd="+projDir, nil)
	rr := httptest.NewRecorder()
	handleApprove(cfg).ServeHTTP(rr, req)
	require.Equal(t, http.StatusNoContent, rr.Code)
	snap, ok, err := store.Read("proj-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Nil(t, snap.MeYAML)
}

func TestApprove_RequiresProjectAndMacCwd(t *testing.T) {
	cfg := identity.Config{Name: "devm-test"}
	rr := httptest.NewRecorder()
	handleApprove(cfg).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/vm/approve", nil))
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestApproveState_RejectsNonGET(t *testing.T) {
	cfg := identity.Config{Name: "devm-test"}
	rr := httptest.NewRecorder()
	handleApproveState(cfg).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/vm/approve-state?project=x&mac_cwd=/tmp/y", nil))
	require.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

func TestApprove_RejectsNonPOST(t *testing.T) {
	cfg := identity.Config{Name: "devm-test"}
	rr := httptest.NewRecorder()
	handleApprove(cfg).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/vm/approve?project=x&mac_cwd=/tmp/y", nil))
	require.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

func TestBootstrapApprovedSnapshotOnFirstRun_WritesInitial(t *testing.T) {
	cfg, projDir, store := approveTestSetup(t, "project:\n  name: p\n", "")
	err := bootstrapApprovedSnapshotOnFirstRun(cfg, "proj-1", projDir)
	require.NoError(t, err)
	snap, ok, err := store.Read("proj-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "project:\n  name: p\n", string(snap.DevmYAML))
	assert.Equal(t, "user", snap.Manifest.Source)
}

func TestBootstrapApprovedSnapshotOnFirstRun_NoOpWhenSnapshotExists(t *testing.T) {
	cfg, projDir, store := approveTestSetup(t, "project:\n  name: p2\n", "")
	require.NoError(t, store.Write("proj-1", []byte("project:\n  name: p\n"), nil, "user"))
	err := bootstrapApprovedSnapshotOnFirstRun(cfg, "proj-1", projDir)
	require.NoError(t, err)
	snap, _, err := store.Read("proj-1")
	require.NoError(t, err)
	assert.Equal(t, "project:\n  name: p\n", string(snap.DevmYAML), "bootstrap must not overwrite an existing snapshot")
}

func TestStart_RefusesWhenDivergedFromApproved(t *testing.T) {
	// A snapshot exists but differs from the current devm.yaml — start must refuse.
	cfg, projDir, store := approveTestSetup(t, "project:\n  name: p\n", "")
	require.NoError(t, store.Write("proj-1", []byte("project:\n  name: old\n"), nil, "user"))
	diverged, err := isApproveDiverged(cfg, "proj-1", projDir)
	require.NoError(t, err)
	assert.True(t, diverged)
}
