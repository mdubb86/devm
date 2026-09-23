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
// devm.me.yaml) into a fresh macCwd, registers it in a fresh
// *StateCache via SetMacCwd, and returns (identityCfg, cache, macCwd,
// snapshotStore).
func approveTestSetup(t *testing.T, project, devm, me string) (identity.Config, *StateCache, string, *approve.Store) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := identity.Config{Name: "devm-test"}
	macCwd := t.TempDir()
	cache := NewStateCache()
	cache.SetMacCwd(project, macCwd)
	require.NoError(t, os.WriteFile(filepath.Join(macCwd, "devm.yaml"), []byte(devm), 0644))
	if me != "" {
		require.NoError(t, os.WriteFile(filepath.Join(macCwd, "devm.me.yaml"), []byte(me), 0644))
	}
	return cfg, cache, macCwd, approve.NewStore(cfg)
}

func TestApproveState_NoSnapshotReportsDiverged(t *testing.T) {
	cfg, cache, _, _ := approveTestSetup(t, "proj-1", "project:\n  name: p\n", "")
	req := httptest.NewRequest(http.MethodGet, "/vm/approve-state?project=proj-1", nil)
	rr := httptest.NewRecorder()
	handleApproveState(cfg, cache).ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["diverged"], "no snapshot → diverged=true (first-run bootstrap territory)")
	assert.Equal(t, "absent", resp["approved_devm_sha"])
	assert.Nil(t, resp["approved_since"])
}

func TestApproveState_SnapshotEqualReportsNotDiverged(t *testing.T) {
	cfg, cache, _, store := approveTestSetup(t, "proj-1", "project:\n  name: p\n", "env:\n  X: 1\n")
	require.NoError(t, store.Write("proj-1", []byte("project:\n  name: p\n"), []byte("env:\n  X: 1\n"), "user"))
	req := httptest.NewRequest(http.MethodGet, "/vm/approve-state?project=proj-1", nil)
	rr := httptest.NewRecorder()
	handleApproveState(cfg, cache).ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, false, resp["diverged"])
	assert.NotNil(t, resp["approved_since"])
}

func TestApproveState_ChangedByteReportsDiverged(t *testing.T) {
	cfg, cache, _, store := approveTestSetup(t, "proj-1", "project:\n  name: p2\n", "")
	require.NoError(t, store.Write("proj-1", []byte("project:\n  name: p\n"), nil, "user"))
	req := httptest.NewRequest(http.MethodGet, "/vm/approve-state?project=proj-1", nil)
	rr := httptest.NewRecorder()
	handleApproveState(cfg, cache).ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["diverged"])
}

func TestApproveState_RequiresProject(t *testing.T) {
	cfg := identity.Config{Name: "devm-test"}
	cache := NewStateCache()
	rr := httptest.NewRecorder()
	handleApproveState(cfg, cache).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/vm/approve-state", nil))
	require.Equal(t, http.StatusBadRequest, rr.Code)
	assert.True(t, strings.Contains(rr.Body.String(), "project"))
}

func TestApproveState_NoMacCwdInCacheReportsNothing(t *testing.T) {
	cfg := identity.Config{Name: "devm-test"}
	cache := NewStateCache()
	req := httptest.NewRequest(http.MethodGet, "/vm/approve-state?project=never-started", nil)
	rr := httptest.NewRecorder()
	handleApproveState(cfg, cache).ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, "an unstarted project is not an error — nothing to report yet")
	var resp approveStateResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, "never-started", resp.Project)
	assert.False(t, resp.Diverged)
	assert.Empty(t, resp.CurrentDevmSHA)
}

func TestApprove_AdvancesSnapshotToCurrentBytes(t *testing.T) {
	cfg, cache, _, store := approveTestSetup(t, "proj-1", "project:\n  name: p\n", "env:\n  X: 1\n")
	req := httptest.NewRequest(http.MethodPost, "/vm/approve?project=proj-1", nil)
	rr := httptest.NewRecorder()
	handleApprove(cfg, cache).ServeHTTP(rr, req)
	require.Equal(t, http.StatusNoContent, rr.Code)
	snap, ok, err := store.Read("proj-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "project:\n  name: p\n", string(snap.DevmYAML))
	assert.Equal(t, "env:\n  X: 1\n", string(snap.MeYAML))
	assert.Equal(t, "user", snap.Manifest.Source)
}

func TestApprove_UpdatesCache(t *testing.T) {
	cfg, cache, _, _ := approveTestSetup(t, "proj-1", "project:\n  name: p\n", "env:\n  X: 1\n")
	req := httptest.NewRequest(http.MethodPost, "/vm/approve?project=proj-1", nil)
	rr := httptest.NewRecorder()
	handleApprove(cfg, cache).ServeHTTP(rr, req)
	require.Equal(t, http.StatusNoContent, rr.Code)

	row, ok := cache.ProjectRow("proj-1")
	require.True(t, ok, "approve must create a cache row for the project")
	assert.False(t, row.ApproveState.Diverged, "a just-approved project can't be diverged")
	assert.Equal(t, row.ApproveState.CurrentDevmSHA, row.ApproveState.ApprovedDevmSHA)
	assert.Equal(t, row.ApproveState.CurrentMeSHA, row.ApproveState.ApprovedMeSHA)
	assert.NotEmpty(t, row.ApproveState.CurrentDevmSHA)
	require.NotNil(t, row.ApproveState.ApprovedSince)
}

func TestApprove_IdempotentOnAlreadyApproved(t *testing.T) {
	cfg, cache, _, store := approveTestSetup(t, "proj-1", "project:\n  name: p\n", "")
	require.NoError(t, store.Write("proj-1", []byte("project:\n  name: p\n"), nil, "user"))
	req := httptest.NewRequest(http.MethodPost, "/vm/approve?project=proj-1", nil)
	rr := httptest.NewRecorder()
	handleApprove(cfg, cache).ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNoContent, rr.Code)
}

func TestApprove_RemovesStaleMeYAMLWhenAbsentOnMac(t *testing.T) {
	cfg, cache, _, store := approveTestSetup(t, "proj-1", "project:\n  name: p\n", "")
	// Prior snapshot has a me.yaml.
	require.NoError(t, store.Write("proj-1", []byte("project:\n  name: p\n"), []byte("env:\n  OLD: 1\n"), "user"))
	// Mac side does not have me.yaml. Approve must remove the old copy from the snapshot.
	req := httptest.NewRequest(http.MethodPost, "/vm/approve?project=proj-1", nil)
	rr := httptest.NewRecorder()
	handleApprove(cfg, cache).ServeHTTP(rr, req)
	require.Equal(t, http.StatusNoContent, rr.Code)
	snap, ok, err := store.Read("proj-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Nil(t, snap.MeYAML)
}

func TestApprove_RequiresProject(t *testing.T) {
	cfg := identity.Config{Name: "devm-test"}
	cache := NewStateCache()
	rr := httptest.NewRecorder()
	handleApprove(cfg, cache).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/vm/approve", nil))
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestApprove_NoMacCwdInCacheReturns412(t *testing.T) {
	cfg := identity.Config{Name: "devm-test"}
	cache := NewStateCache()
	req := httptest.NewRequest(http.MethodPost, "/vm/approve?project=never-started", nil)
	rr := httptest.NewRecorder()
	handleApprove(cfg, cache).ServeHTTP(rr, req)
	assert.Equal(t, http.StatusPreconditionFailed, rr.Code)
	assert.Contains(t, rr.Body.String(), "never-started")
}

func TestApproveState_RejectsNonGET(t *testing.T) {
	cfg := identity.Config{Name: "devm-test"}
	cache := NewStateCache()
	rr := httptest.NewRecorder()
	handleApproveState(cfg, cache).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/vm/approve-state?project=x", nil))
	require.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

func TestApprove_RejectsNonPOST(t *testing.T) {
	cfg := identity.Config{Name: "devm-test"}
	cache := NewStateCache()
	rr := httptest.NewRecorder()
	handleApprove(cfg, cache).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/vm/approve?project=x", nil))
	require.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

func TestBootstrapApprovedSnapshotOnFirstRun_WritesInitial(t *testing.T) {
	cfg, _, macCwd, store := approveTestSetup(t, "proj-1", "project:\n  name: p\n", "")
	err := bootstrapApprovedSnapshotOnFirstRun(cfg, "proj-1", macCwd)
	require.NoError(t, err)
	snap, ok, err := store.Read("proj-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "project:\n  name: p\n", string(snap.DevmYAML))
	assert.Equal(t, "user", snap.Manifest.Source)
}

func TestBootstrapApprovedSnapshotOnFirstRun_NoOpWhenSnapshotExists(t *testing.T) {
	cfg, _, macCwd, store := approveTestSetup(t, "proj-1", "project:\n  name: p2\n", "")
	require.NoError(t, store.Write("proj-1", []byte("project:\n  name: p\n"), nil, "user"))
	err := bootstrapApprovedSnapshotOnFirstRun(cfg, "proj-1", macCwd)
	require.NoError(t, err)
	snap, _, err := store.Read("proj-1")
	require.NoError(t, err)
	assert.Equal(t, "project:\n  name: p\n", string(snap.DevmYAML), "bootstrap must not overwrite an existing snapshot")
}

func TestStart_RefusesWhenDivergedFromApproved(t *testing.T) {
	// A snapshot exists but differs from the current devm.yaml — start must refuse.
	cfg, _, macCwd, store := approveTestSetup(t, "proj-1", "project:\n  name: p\n", "")
	require.NoError(t, store.Write("proj-1", []byte("project:\n  name: old\n"), nil, "user"))
	diverged, err := isApproveDiverged(cfg, "proj-1", macCwd)
	require.NoError(t, err)
	assert.True(t, diverged)
}

func TestApproveState_IncludesProposalWhenPresent(t *testing.T) {
	cfg, cache, _, _ := approveTestSetup(t, "proj", "name: p\n", "")

	// Seed a proposal.
	require.NoError(t, WriteLastProposal(cfg, "proj", ProposalMetadata{
		Cwd:       "/home/devm/proj",
		Branch:    "feature-x",
		Reason:    "test reason",
		Timestamp: "2026-09-14T12:00:00Z",
		Source:    "guest",
		Kind:      "devm.yaml",
	}))

	req := httptest.NewRequest(http.MethodGet, "/vm/approve-state?project=proj", nil)
	rr := httptest.NewRecorder()
	handleApproveState(cfg, cache).ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var resp approveStateResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	require.NotNil(t, resp.Proposal)
	assert.Equal(t, "feature-x", resp.Proposal.Branch)
	assert.Equal(t, "test reason", resp.Proposal.Reason)
	assert.Equal(t, "guest", resp.Proposal.Source)
}

func TestApprove_ClearsProposalOnSuccess(t *testing.T) {
	cfg, cache, _, _ := approveTestSetup(t, "proj", "name: p\n", "")

	// Seed a proposal.
	require.NoError(t, WriteLastProposal(cfg, "proj", ProposalMetadata{
		Cwd:       "/x",
		Reason:    "x",
		Timestamp: "2026-09-14T00:00:00Z",
		Source:    "guest",
		Kind:      "devm.yaml",
	}))

	req := httptest.NewRequest(http.MethodPost, "/vm/approve?project=proj", nil)
	rr := httptest.NewRecorder()
	handleApprove(cfg, cache).ServeHTTP(rr, req)
	require.Equal(t, http.StatusNoContent, rr.Code)

	_, ok, err := ReadLastProposal(cfg, "proj")
	require.NoError(t, err)
	assert.False(t, ok, "proposal must be cleared after successful approve")
}
