package serviceapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type resolveResponse struct {
	Name     string `json:"name"`
	StateDir string `json:"state_dir"`
	Cwd      string `json:"cwd"`
}

func TestResolveProject_Match(t *testing.T) {
	cfg := identity.Prod
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, AddCwdAlias(cfg, "shelfmates", "/Users/x/code/shelfmates"))

	req := httptest.NewRequest(http.MethodPost, "/vm/resolve-project?cwd=/Users/x/code/shelfmates", nil)
	rr := httptest.NewRecorder()
	handleResolveProject(cfg).ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())
	var resp resolveResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, "shelfmates", resp.Name)
	assert.Contains(t, resp.StateDir, "shelfmates")
	assert.Equal(t, "/Users/x/code/shelfmates", resp.Cwd)
}

// TestResolveProject_MatchWalksUpToAncestor pins I1's fix: a call from
// a subdirectory of a registered project must return the registered
// ancestor cwd (not the raw request cwd) so downstream CLI verbs treat
// the project root, not the subdir, as repoRoot.
func TestResolveProject_MatchWalksUpToAncestor(t *testing.T) {
	cfg := identity.Prod
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, AddCwdAlias(cfg, "proj", "/Users/x/proj"))

	req := httptest.NewRequest(http.MethodPost, "/vm/resolve-project?cwd=/Users/x/proj/subdir/deeper", nil)
	rr := httptest.NewRecorder()
	handleResolveProject(cfg).ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())
	var resp resolveResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, "proj", resp.Name)
	assert.Equal(t, "/Users/x/proj", resp.Cwd)
}

func TestResolveProject_Miss404(t *testing.T) {
	cfg := identity.Prod
	t.Setenv("HOME", t.TempDir())

	req := httptest.NewRequest(http.MethodPost, "/vm/resolve-project?cwd=/nowhere", nil)
	rr := httptest.NewRecorder()
	handleResolveProject(cfg).ServeHTTP(rr, req)

	assert.Equal(t, http.StatusNotFound, rr.Code)
	assert.Contains(t, rr.Body.String(), "devm init")
}

func TestResolveProject_MissingCwdParam400(t *testing.T) {
	cfg := identity.Prod
	req := httptest.NewRequest(http.MethodPost, "/vm/resolve-project", nil)
	rr := httptest.NewRecorder()
	handleResolveProject(cfg).ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestResolveProject_MethodNotAllowed(t *testing.T) {
	cfg := identity.Prod
	req := httptest.NewRequest(http.MethodGet, "/vm/resolve-project?cwd=/x", nil)
	rr := httptest.NewRecorder()
	handleResolveProject(cfg).ServeHTTP(rr, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}
