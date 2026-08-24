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

func writeWorkspaceSnapshot(t *testing.T, projectID, macCwd string, repo *schema.RepoConfig) {
	t.Helper()
	require.NoError(t, WriteStateSnapshot(identity.Prod, projectID, StateSnapshot{
		Cfg: schema.Config{
			Project: schema.Project{Name: projectID},
			Repo:    repo,
		},
		WorkspaceHostPath: macCwd,
	}))
}

func TestResolveEndpoint_ListsPrimaryWorkspaces(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	writeWorkspaceSnapshot(t, "sewtrue", "/Users/me/projects/sewtrue", &schema.RepoConfig{Secret: "gh"})
	writeWorkspaceSnapshot(t, "foo", "/Users/me/projects/foo", &schema.RepoConfig{Secret: "gh"})

	srv := NewServer(identity.Prod.SocketPath(), Build{Version: "dev"})
	RegisterWorkspacesHandler(srv, identity.Prod)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/workspaces", nil)
	srv.mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	var got []WorkspaceEntry
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Len(t, got, 2)
	for _, w := range got {
		assert.NotEmpty(t, w.Project)
		assert.NotEmpty(t, w.GuestPath)
		assert.NotEmpty(t, w.StoragePath)
	}
}

func TestResolveEndpoint_SkipsProjectsWithoutPrimaryRepo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	writeWorkspaceSnapshot(t, "sewtrue", "/Users/me/projects/sewtrue", &schema.RepoConfig{Secret: "gh"})
	writeWorkspaceSnapshot(t, "no-repo", "/Users/me/projects/no-repo", nil)

	srv := NewServer(identity.Prod.SocketPath(), Build{Version: "dev"})
	RegisterWorkspacesHandler(srv, identity.Prod)

	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest("GET", "/workspaces", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	var got []WorkspaceEntry
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got, 1)
	assert.Equal(t, "sewtrue", got[0].Project)
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
