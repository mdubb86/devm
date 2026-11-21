package serviceapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubPopExecOpen records every open invocation; the returned restore
// func undoes the swap. Serialised across tests via popExecOpenMu so
// parallel subtests don't race the single package-level func var.
var popExecOpenMu sync.Mutex

func stubPopExecOpen(t *testing.T) *[]string {
	t.Helper()
	popExecOpenMu.Lock()
	orig := popExecOpen
	captured := []string{}
	popExecOpen = func(_ context.Context, args ...string) error {
		captured = append(captured, args...)
		return nil
	}
	t.Cleanup(func() {
		popExecOpen = orig
		popExecOpenMu.Unlock()
	})
	return &captured
}

func TestHandlePop_Default_OpensFilestashViewURL(t *testing.T) {
	captured := stubPopExecOpen(t)

	body, err := json.Marshal(PopRequest{GuestPath: "/home/devm/foo.html"})
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/pop", strings.NewReader(string(body)))
	handlePop(rec, req, identity.Prod, "myproj")

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	require.Len(t, *captured, 1, "default opens exactly one arg (URL)")
	assert.Equal(t, "https://files.myproj.test/view/home/devm/foo.html", (*captured)[0])
	assert.Equal(t, "https://files.myproj.test/view/home/devm/foo.html\n", rec.Body.String())
}

func TestHandlePop_IsHTML_RoutesToPreview(t *testing.T) {
	captured := stubPopExecOpen(t)
	body, err := json.Marshal(PopRequest{GuestPath: "/home/devm/x.html", IsHTML: true})
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/pop", strings.NewReader(string(body)))
	handlePop(rec, req, identity.Prod, "myproj")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []string{"https://preview.myproj.test/home/devm/x.html"}, *captured)
}

func TestHandlePop_IsDir_RoutesToFilestashListing(t *testing.T) {
	captured := stubPopExecOpen(t)
	body, err := json.Marshal(PopRequest{GuestPath: "/home/devm/dir", IsDir: true})
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/pop", strings.NewReader(string(body)))
	handlePop(rec, req, identity.Prod, "myproj")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []string{"https://files.myproj.test/files/home/devm/dir/"}, *captured)
}

func TestHandlePop_Default_WithOpenArgs(t *testing.T) {
	captured := stubPopExecOpen(t)
	body, err := json.Marshal(PopRequest{
		GuestPath: "/home/devm/foo.html",
		OpenArgs:  []string{"-a", "Google Chrome"},
	})
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/pop", strings.NewReader(string(body)))
	handlePop(rec, req, identity.Prod, "myproj")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []string{
		"https://files.myproj.test/view/home/devm/foo.html",
		"-a", "Google Chrome",
	}, *captured)
}

func TestHandlePop_RejectsNonPOST(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/pop", nil)
	handlePop(rec, req, identity.Prod, "myproj")
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestHandlePop_RejectsRelativePath(t *testing.T) {
	stubPopExecOpen(t)
	body, _ := json.Marshal(PopRequest{GuestPath: "relative/foo.html"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/pop", strings.NewReader(string(body)))
	handlePop(rec, req, identity.Prod, "myproj")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "must be absolute")
}

func TestHandlePop_RejectsMissingPath(t *testing.T) {
	stubPopExecOpen(t)
	body, _ := json.Marshal(PopRequest{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/pop", strings.NewReader(string(body)))
	handlePop(rec, req, identity.Prod, "myproj")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestResolvePopTarget_Default_BuildsFilestashViewURL(t *testing.T) {
	got, err := resolvePopTarget(context.Background(), identity.Prod, "proj", "/home/devm/path/with spaces.txt", false, false, false)
	require.NoError(t, err)
	assert.Equal(t, "https://files.proj.test/view/home/devm/path/with%20spaces.txt", got)
}

func TestPopTargetURL_UsesTLDFromIdentity(t *testing.T) {
	assert.Equal(t, "https://files.proj.e2e.test/view/a/b", popTargetURL(identity.E2E, "proj", "/a/b", false, false))
	assert.Equal(t, "https://files.proj.test/view/a/b", popTargetURL(identity.Prod, "proj", "/a/b", false, false))
}

func TestPopTargetURL_HTMLGoesToPreview(t *testing.T) {
	cfg := identity.Config{TLD: "test"}
	u := popTargetURL(cfg, "sewtrue", "/home/devm/sewtrue/sketch/index.html", false, true)
	assert.Equal(t, "https://preview.sewtrue.test/home/devm/sewtrue/sketch/index.html", u)
}

func TestPopTargetURL_DirectoryGoesToFilestashListing(t *testing.T) {
	cfg := identity.Config{TLD: "test"}
	u := popTargetURL(cfg, "sewtrue", "/home/devm/sewtrue", true, false)
	assert.Equal(t, "https://files.sewtrue.test/files/home/devm/sewtrue/", u)
}

func TestPopTargetURL_OtherFileGoesToFilestashView(t *testing.T) {
	cfg := identity.Config{TLD: "test"}
	u := popTargetURL(cfg, "sewtrue", "/home/devm/sewtrue/image.png", false, false)
	assert.Equal(t, "https://files.sewtrue.test/view/home/devm/sewtrue/image.png", u)
}

// A caller that mis-classifies a path as both must still get a page, not a listing.
func TestPopTargetURL_HTMLWinsOverIsDirConflict(t *testing.T) {
	cfg := identity.Config{TLD: "test"}
	u := popTargetURL(cfg, "sewtrue", "/home/devm/sewtrue/sketch/index.html", true, true)
	assert.Equal(t, "https://preview.sewtrue.test/home/devm/sewtrue/sketch/index.html", u)
}

func TestPopScratchName_StablePerProjectAndPath(t *testing.T) {
	a := popScratchName("proj", "/home/devm/foo.html")
	b := popScratchName("proj", "/home/devm/foo.html")
	assert.Equal(t, a, b, "same input must hash to same scratch name")
	c := popScratchName("proj2", "/home/devm/foo.html")
	assert.NotEqual(t, a, c, "different project must differ")
	d := popScratchName("proj", "/home/devm/bar.html")
	assert.NotEqual(t, a, d, "different path must differ")
	assert.True(t, strings.HasSuffix(a, ".html"), "preserves extension so `open` picks the right app")
}

func TestResolvePopTarget_Native_OutOfMirror_UsesScratchCopy(t *testing.T) {
	// Simulate an out-of-mirror guest path: empty workspace registry
	// (no mirrors), tart-exec-cat stubbed to write a known byte string.
	cfg := identity.Prod
	homeTmp := t.TempDir()
	t.Setenv("HOME", homeTmp)

	origCat := popTartExecCat
	t.Cleanup(func() { popTartExecCat = origCat })
	popTartExecCat = func(_ context.Context, vm, guestPath, macDest string) error {
		return os.WriteFile(macDest, []byte("hello from guest"), 0o644)
	}

	got, err := resolvePopTarget(context.Background(), cfg, "proj", "/opt/elsewhere/notes.md", true, false, false)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(got, PopScratchRoot(cfg)), "scratch target must live under PopScratchRoot: got %s", got)
	assert.True(t, strings.HasSuffix(got, ".md"), "extension preserved so Preview picks the right handler")
	b, err := os.ReadFile(got)
	require.NoError(t, err)
	assert.Equal(t, "hello from guest", string(b))
	// Clean up the scratch dir we leaked into HOME.
	_ = os.RemoveAll(filepath.Dir(got))
}
