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

func writeWorkspaceSnapshot(t *testing.T, projectID, macCwd string) {
	t.Helper()
	require.NoError(t, WriteStateSnapshot(identity.Prod, projectID, StateSnapshot{
		Cfg: schema.Config{
			Project: schema.Project{Name: projectID},
		},
		WorkspaceHostPath: macCwd,
	}))
}

// TestResolveEndpoint_NoOpUntilReposMap pins listWorkspaces' current
// TODO(Task 17) interim behavior: /workspaces reports no entries at
// all, even for projects with persisted snapshots, until the
// primary-repo lookup is rewritten against Cfg.Repos.
func TestResolveEndpoint_NoOpUntilReposMap(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	writeWorkspaceSnapshot(t, "sewtrue", "/Users/me/projects/sewtrue")

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
