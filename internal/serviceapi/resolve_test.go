package serviceapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeWorkspaceSnapshot(t *testing.T, projectID string) {
	t.Helper()
	require.NoError(t, WriteStateSnapshot(identity.Prod, projectID, StateSnapshot{
		Cfg: schema.Config{
			Project: schema.Project{Name: projectID},
		},
	}))
}

// TestResolveEndpoint_ProjectWithNoMirroredEntries_EmptyList — a
// persisted snapshot with no repos/volumes contributes no entries.
func TestResolveEndpoint_ProjectWithNoMirroredEntries_EmptyList(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	writeWorkspaceSnapshot(t, "sewtrue")

	srv := NewServer(identity.Prod.SocketPath(), Build{Version: "dev"})
	RegisterWorkspacesHandler(srv, identity.Prod)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/workspaces", nil)
	srv.mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	var got []WorkspaceEntry
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Empty(t, got)
}

func TestResolveEndpoint_NoSnapshots_EmptyList(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	srv := NewServer(identity.Prod.SocketPath(), Build{Version: "dev"})
	RegisterWorkspacesHandler(srv, identity.Prod)

	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest("GET", "/workspaces", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	var got []WorkspaceEntry
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Empty(t, got)
}

// TestResolveEndpoint_ReturnsRepoAndVolumeEntries exercises the full
// HTTP path: a project with a primary repo and a volume reports one
// WorkspaceEntry each, with Label/GuestPath/StoragePath populated.
func TestResolveEndpoint_ReturnsRepoAndVolumeEntries(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	url := "https://example.com/app.git"
	primary := true
	require.NoError(t, WriteStateSnapshot(identity.Prod, "proj1", StateSnapshot{
		Cfg: schema.Config{
			Project: schema.Project{Name: "proj1"},
			Repos: map[string]schema.RepoConfig{
				"main": {URL: &url, Primary: &primary},
			},
			Volumes: map[string]schema.Volume{
				"cache": {Path: "/var/cache/data"},
			},
		},
	}))

	srv := NewServer(identity.Prod.SocketPath(), Build{Version: "dev"})
	RegisterWorkspacesHandler(srv, identity.Prod)

	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest("GET", "/workspaces", nil))
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	var got []WorkspaceEntry
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got, 2)

	byLabel := map[string]WorkspaceEntry{}
	for _, e := range got {
		byLabel[e.Label] = e
	}

	repoEntry, ok := byLabel["app"]
	require.True(t, ok, "expected a %q entry, got %+v", "app", got)
	assert.Equal(t, "proj1", repoEntry.ProjectName)
	assert.Equal(t, "/home/devm/app", repoEntry.GuestPath)
	assert.Equal(t, mirrorMacDir(identity.Prod, "proj1", "app"), repoEntry.StoragePath)

	volEntry, ok := byLabel["data"]
	require.True(t, ok, "expected a %q entry, got %+v", "data", got)
	assert.Equal(t, "/var/cache/data", volEntry.GuestPath)
	assert.Equal(t, mirrorMacDir(identity.Prod, "proj1", "data"), volEntry.StoragePath)
}

// TestListWorkspaces_URLNilLabelNilPrimary_FallsBackToProjectID pins
// the one deliberate divergence from BuildEntities' label rules:
// macCwd isn't persisted on StateSnapshot, so a primary repo with
// neither `url:` nor `label:` set falls back to projectID rather than
// the Mac checkout's basename.
func TestListWorkspaces_URLNilLabelNilPrimary_FallsBackToProjectID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	require.NoError(t, WriteStateSnapshot(identity.Prod, "sewtrue", StateSnapshot{
		Cfg: schema.Config{
			Project: schema.Project{Name: "sewtrue"},
			Repos: map[string]schema.RepoConfig{
				"main": {}, // no url, no label — sole repo is primary by default
			},
		},
	}))

	got, err := listWorkspaces(identity.Prod)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "sewtrue", got[0].Label)
	assert.Equal(t, "/home/devm/sewtrue", got[0].GuestPath)
}

// TestListWorkspaces_SecondaryRepoOnlyWhenVolumeTrue confirms
// secondary repos are excluded unless they opt in with `volume: true`,
// while the primary is included by default.
func TestListWorkspaces_SecondaryRepoOnlyWhenVolumeTrue(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	mainURL := "https://example.com/main.git"
	secURL := "https://example.com/secondary.git"
	primary := true
	volTrue := true
	require.NoError(t, WriteStateSnapshot(identity.Prod, "multi", StateSnapshot{
		Cfg: schema.Config{
			Project: schema.Project{Name: "multi"},
			Repos: map[string]schema.RepoConfig{
				"main":      {URL: &mainURL, Primary: &primary},
				"secondary": {URL: &secURL, Volume: &volTrue},
				"excluded":  {URL: &secURL},
			},
		},
	}))

	got, err := listWorkspaces(identity.Prod)
	require.NoError(t, err)

	var labels []string
	for _, e := range got {
		labels = append(labels, e.Label)
	}
	assert.ElementsMatch(t, []string{"main", "secondary"}, labels)
}
