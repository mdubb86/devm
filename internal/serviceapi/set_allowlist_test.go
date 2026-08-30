package serviceapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetAllowlistHandler_UpdatesAuthorityAndSnapshot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Swap the package-level policyAuthority for isolation.
	orig := policyAuthority
	policyAuthority = NewPolicyAuthority()
	defer func() { policyAuthority = orig }()

	// Seed a snapshot with an empty allowlist so we can prove the
	// update took, mirroring the cold-start-already-happened contract
	// updateSnapshotAfterAllowlistSet requires.
	seededCfg := schema.Config{Project: schema.Project{Name: "proj1"}}
	require.NoError(t, WriteStateSnapshot(identity.Prod, "proj1", StateSnapshot{Cfg: seededCfg}))

	srv := NewServer(identity.Prod.SocketPath(), Build{})
	locks := NewProjectLocks()
	RegisterSetAllowlistHandler(srv, identity.Prod, locks)

	body, _ := json.Marshal(VMSetAllowlistRequest{
		Name:      "proj1",
		Allowlist: []string{"example.com", "api.github.com"},
	})
	req := httptest.NewRequest(http.MethodPost, "/vm/set-allowlist", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code, "body=%s", rec.Body.String())

	// Authority sees the new allowlist immediately.
	got := policyAuthority.allowlistFor("proj1")
	assert.Equal(t, []string{"example.com", "api.github.com"}, got)

	// Snapshot persisted the same allowlist into snap.Cfg.Network.Allow
	// — the field recoverProjectState reads back on daemon restart.
	snap, err := ReadStateSnapshot(identity.Prod, "proj1")
	require.NoError(t, err)
	require.NotNil(t, snap)
	require.Len(t, snap.Cfg.Network.Allow, 2)
	assert.Equal(t, "example.com", snap.Cfg.Network.Allow[0].Host)
	assert.Equal(t, "api.github.com", snap.Cfg.Network.Allow[1].Host)
}

func TestSetAllowlistHandler_RejectsMissingName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	srv := NewServer(identity.Prod.SocketPath(), Build{})
	locks := NewProjectLocks()
	RegisterSetAllowlistHandler(srv, identity.Prod, locks)

	body, _ := json.Marshal(map[string]any{"allowlist": []string{"x.com"}})
	req := httptest.NewRequest(http.MethodPost, "/vm/set-allowlist", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestInProcessAllowlistSetter_UpdatesAuthorityAndSnapshotWithoutLock
// proves the reconcile-path adapter reaches the same two effects as
// the HTTP handler (policyAuthority.Set + snapshot rewrite) without
// acquiring any ProjectLocks lock — the /vm/reconcile handler already
// holds req.Name's lock for the ApplyLive call this adapter is reached
// from, so a second acquisition here would deadlock.
func TestInProcessAllowlistSetter_UpdatesAuthorityAndSnapshotWithoutLock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	orig := policyAuthority
	policyAuthority = NewPolicyAuthority()
	defer func() { policyAuthority = orig }()

	seededCfg := schema.Config{Project: schema.Project{Name: "proj1"}}
	require.NoError(t, WriteStateSnapshot(identity.Prod, "proj1", StateSnapshot{Cfg: seededCfg}))

	// Deliberately do NOT construct a ProjectLocks or call Lock: the
	// adapter must not need one. If SetAllowlist below tried to
	// re-acquire req.Name's lock the way the HTTP handler does, there
	// would be no lock to acquire in this test at all — the assertion
	// is that it doesn't try.
	setter := &inProcessAllowlistSetter{cfg: identity.Prod}
	err := setter.SetAllowlist(context.Background(), "proj1", []string{"example.com", "api.github.com"})
	require.NoError(t, err)

	got := policyAuthority.allowlistFor("proj1")
	assert.Equal(t, []string{"example.com", "api.github.com"}, got)

	snap, err := ReadStateSnapshot(identity.Prod, "proj1")
	require.NoError(t, err)
	require.NotNil(t, snap)
	require.Len(t, snap.Cfg.Network.Allow, 2)
	assert.Equal(t, "example.com", snap.Cfg.Network.Allow[0].Host)
	assert.Equal(t, "api.github.com", snap.Cfg.Network.Allow[1].Host)
}
