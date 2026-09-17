package serviceapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegisterProject_FreshCreatesSeedAndRegistersCwd(t *testing.T) {
	cfg := identity.Prod
	t.Setenv("HOME", t.TempDir())

	req := httptest.NewRequest(http.MethodPost, "/vm/register-project?name=shelfmates&cwd=/Users/x/shelfmates", nil)
	rr := httptest.NewRecorder()
	handleRegisterProject(cfg).ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())

	// Seed exists.
	body, err := os.ReadFile(filepath.Join(stateDirForProject(cfg, "shelfmates"), "devm.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(body), "name: shelfmates")

	// Alias registered.
	aliases, _ := ReadCwdAliases(cfg, "shelfmates")
	assert.Equal(t, []string{"/Users/x/shelfmates"}, aliases)
}

func TestRegisterProject_AlreadyExistsReturnsConflict(t *testing.T) {
	cfg := identity.Prod
	t.Setenv("HOME", t.TempDir())

	// Seed once.
	req := httptest.NewRequest(http.MethodPost, "/vm/register-project?name=p&cwd=/x", nil)
	rr := httptest.NewRecorder()
	handleRegisterProject(cfg).ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	// Second time → 409.
	req = httptest.NewRequest(http.MethodPost, "/vm/register-project?name=p&cwd=/x", nil)
	rr = httptest.NewRecorder()
	handleRegisterProject(cfg).ServeHTTP(rr, req)
	assert.Equal(t, http.StatusConflict, rr.Code)
	assert.Contains(t, rr.Body.String(), "already exists")
}

func TestRegisterProject_CwdAlreadyRegisteredElsewhere_Conflict(t *testing.T) {
	cfg := identity.Prod
	t.Setenv("HOME", t.TempDir())

	require.NoError(t, AddCwdAlias(cfg, "alpha", "/Users/x/code"))

	req := httptest.NewRequest(http.MethodPost, "/vm/register-project?name=beta&cwd=/Users/x/code", nil)
	rr := httptest.NewRecorder()
	handleRegisterProject(cfg).ServeHTTP(rr, req)
	assert.Equal(t, http.StatusConflict, rr.Code)
	assert.Contains(t, rr.Body.String(), "already registered as")
}

func TestRegisterProject_MissingNameParam400(t *testing.T) {
	cfg := identity.Prod
	req := httptest.NewRequest(http.MethodPost, "/vm/register-project?cwd=/x", nil)
	rr := httptest.NewRecorder()
	handleRegisterProject(cfg).ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestRegisterProject_MissingCwdParam400(t *testing.T) {
	cfg := identity.Prod
	req := httptest.NewRequest(http.MethodPost, "/vm/register-project?name=p", nil)
	rr := httptest.NewRecorder()
	handleRegisterProject(cfg).ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestRegisterProject_MethodNotAllowed(t *testing.T) {
	cfg := identity.Prod
	req := httptest.NewRequest(http.MethodGet, "/vm/register-project?name=p&cwd=/x", nil)
	rr := httptest.NewRecorder()
	handleRegisterProject(cfg).ServeHTTP(rr, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}
