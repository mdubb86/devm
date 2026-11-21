package serviceapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeExecStdinTart returns a *tart.Tart whose Path points at a
// script that always exits 0 on `exec -i` (the transport used to
// pipe the bundle in). Mirrors the pattern in existing serviceapi
// tests (see e.g. state_test.go for a similar shape).
func fakeExecStdinTart(t *testing.T) *tart.Tart {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "tart-fake")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nexec cat >/dev/null\n"), 0o755))
	tr := tart.New()
	tr.Path = bin
	return tr
}

func TestRefreshGuestBundle_UpdatesStateSnapshotFingerprint(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := identity.Prod
	cache := NewStateCache()
	cache.SetBuild(Build{Fingerprint: "new-fp"})

	// Seed a stored snapshot with an older fingerprint.
	require.NoError(t, WriteStateSnapshot(cfg, "proj", StateSnapshot{
		Cfg:               schema.Config{Project: schema.Project{Name: "proj"}},
		BundleFingerprint: "old-fp",
	}))

	tr := fakeExecStdinTart(t)
	summary, err := RefreshGuestBundle(context.Background(), cfg, cache, tr, "proj")
	require.NoError(t, err)

	assert.Equal(t, "proj", summary.ProjectID)
	assert.Equal(t, "old-fp", summary.OldFingerprint)
	assert.Equal(t, "new-fp", summary.NewFingerprint)

	// Snapshot was persisted with the new fingerprint.
	stored, err := ReadStateSnapshot(cfg, "proj")
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, "new-fp", stored.BundleFingerprint)
}

func TestRefreshGuestBundle_MissingSnapshotReturns412ish(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := identity.Prod
	cache := NewStateCache()
	cache.SetBuild(Build{Fingerprint: "new-fp"})

	tr := fakeExecStdinTart(t)
	_, err := RefreshGuestBundle(context.Background(), cfg, cache, tr, "no-such-proj")
	require.Error(t, err, "no snapshot → error (VM was never provisioned; cold-start writes the initial snapshot)")
}

func TestRefreshGuestBundle_MissingSnapshotErrorSatisfiesSentinel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := identity.Prod
	cache := NewStateCache()
	cache.SetBuild(Build{Fingerprint: "new-fp"})
	tr := fakeExecStdinTart(t)

	_, err := RefreshGuestBundle(context.Background(), cfg, cache, tr, "nonexistent")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNoStateSnapshot),
		"missing-snapshot error must satisfy errors.Is(ErrNoStateSnapshot) so callers can dispatch precisely")
}

func TestRefreshGuestBundle_TartPipeFailureDoesNotStampFingerprint(t *testing.T) {
	// A tart binary that exits 1 on exec must NOT leave the
	// snapshot's BundleFingerprint updated — otherwise a later
	// reconcile would think refresh succeeded and skip it.
	t.Setenv("HOME", t.TempDir())
	cfg := identity.Prod
	cache := NewStateCache()
	cache.SetBuild(Build{Fingerprint: "new-fp"})

	require.NoError(t, WriteStateSnapshot(cfg, "proj", StateSnapshot{
		Cfg:               schema.Config{Project: schema.Project{Name: "proj"}},
		BundleFingerprint: "old-fp",
	}))

	bin := filepath.Join(t.TempDir(), "tart-fail")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nexit 1\n"), 0o755))
	tr := tart.New()
	tr.Path = bin

	_, err := RefreshGuestBundle(context.Background(), cfg, cache, tr, "proj")
	require.Error(t, err, "tart failure must surface as an error")

	stored, err := ReadStateSnapshot(cfg, "proj")
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, "old-fp", stored.BundleFingerprint, "failed refresh must NOT stamp new fingerprint")
}

func TestRefreshBundleHandler_HappyPathReturns200WithSummary(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := identity.Prod
	cache := NewStateCache()
	cache.SetBuild(Build{Fingerprint: "new-fp"})

	require.NoError(t, WriteStateSnapshot(cfg, "proj", StateSnapshot{
		Cfg:               schema.Config{Project: schema.Project{Name: "proj"}},
		BundleFingerprint: "old-fp",
	}))
	tr := fakeExecStdinTart(t)

	h := handleRefreshBundleForProject(cfg, cache, tr, NewProjectLocks(), "proj")
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/refresh-bundle", nil)
	h.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "bundle refreshed")
	assert.Contains(t, rr.Body.String(), "old-fp")
	assert.Contains(t, rr.Body.String(), "new-fp")
}

func TestRefreshBundleHandler_MethodNotPostReturns405(t *testing.T) {
	h := handleRefreshBundleForProject(identity.Prod, NewStateCache(), &tart.Tart{}, NewProjectLocks(), "proj")
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/refresh-bundle", nil)
	h.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

func TestRefreshBundleHandler_MissingSnapshotReturns412(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := identity.Prod
	cache := NewStateCache()
	cache.SetBuild(Build{Fingerprint: "new-fp"})
	tr := fakeExecStdinTart(t)

	h := handleRefreshBundleForProject(cfg, cache, tr, NewProjectLocks(), "no-such-proj")
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/refresh-bundle", nil)
	h.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusPreconditionFailed, rr.Code)
	assert.Contains(t, rr.Body.String(), "VM must be started")
}
