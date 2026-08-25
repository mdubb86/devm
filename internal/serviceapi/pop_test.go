package serviceapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// popExecSeamCapture replaces the `open` invocation with a recorder for
// tests. Mirrors ironProxySpawn's injection pattern in ironproxy.go.
type popExecRecord struct {
	name string
	args []string
}

func withPopExecSeam(t *testing.T, fn func(recs *[]popExecRecord)) {
	t.Helper()
	var recs []popExecRecord
	orig := popExecOpen
	popExecOpen = func(ctx context.Context, args ...string) error {
		recs = append(recs, popExecRecord{name: "open", args: args})
		return nil
	}
	t.Cleanup(func() { popExecOpen = orig })
	fn(&recs)
}

func TestPopHandler_ResolvesCwdRelative_OpensPrettyPath(t *testing.T) {
	storage := t.TempDir()
	guestRoot := "/Users/x/proj"
	// Seed a file in storage matching where the guest cwd/arg would land.
	subInStorage := filepath.Join(storage, "src")
	require.NoError(t, os.MkdirAll(subInStorage, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(subInStorage, "foo.png"), []byte("x"), 0o644))

	registry := []WorkspaceEntry{{ProjectName: "p", GuestPath: guestRoot, StoragePath: storage}}

	withPopExecSeam(t, func(recs *[]popExecRecord) {
		body, _ := json.Marshal(map[string]any{
			"arg": "foo.png",
			"cwd": guestRoot + "/src",
		})
		req := httptest.NewRequest(http.MethodPost, "/pop", bytes.NewReader(body))
		w := httptest.NewRecorder()
		handlePop(w, req, "p", registry)

		assert.Equal(t, 200, w.Code, "body: %s", w.Body.String())
		require.Len(t, *recs, 1)
		require.Len(t, (*recs)[0].args, 1)
		assert.Equal(t, guestRoot+"/.vm/src/foo.png", (*recs)[0].args[0],
			"open must receive the pretty .vm/-form path")
	})
}

func TestPopHandler_FallsBackToProjectRoot(t *testing.T) {
	storage := t.TempDir()
	guestRoot := "/Users/x/proj"
	// File at project root only; not in cwd subdir.
	require.NoError(t, os.WriteFile(filepath.Join(storage, "top.png"), []byte("x"), 0o644))

	registry := []WorkspaceEntry{{ProjectName: "p", GuestPath: guestRoot, StoragePath: storage}}

	withPopExecSeam(t, func(recs *[]popExecRecord) {
		body, _ := json.Marshal(map[string]any{
			"arg": "top.png",
			"cwd": guestRoot + "/src/deep",
		})
		req := httptest.NewRequest(http.MethodPost, "/pop", bytes.NewReader(body))
		w := httptest.NewRecorder()
		handlePop(w, req, "p", registry)

		assert.Equal(t, 200, w.Code, "body: %s", w.Body.String())
		require.Len(t, *recs, 1)
		assert.Equal(t, guestRoot+"/.vm/top.png", (*recs)[0].args[0])
	})
}

func TestPopHandler_NotFound_404(t *testing.T) {
	storage := t.TempDir()
	guestRoot := "/Users/x/proj"
	registry := []WorkspaceEntry{{ProjectName: "p", GuestPath: guestRoot, StoragePath: storage}}

	body, _ := json.Marshal(map[string]any{
		"arg": "nonexistent.png",
		"cwd": guestRoot,
	})
	req := httptest.NewRequest(http.MethodPost, "/pop", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handlePop(w, req, "p", registry)

	assert.Equal(t, 404, w.Code)
	assert.Contains(t, w.Body.String(), "no such file")
}

func TestPopHandler_CwdOutsideGuestPath_400(t *testing.T) {
	storage := t.TempDir()
	registry := []WorkspaceEntry{
		{ProjectName: "p", GuestPath: "/Users/x/proj", StoragePath: storage},
	}
	body, _ := json.Marshal(map[string]any{
		"arg": "foo.png",
		"cwd": "/Users/x/some-other-place",
	})
	req := httptest.NewRequest(http.MethodPost, "/pop", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handlePop(w, req, "p", registry)

	assert.Equal(t, 400, w.Code)
	assert.Contains(t, w.Body.String(), "outside project")
}

func TestPopHandler_ForwardsOpenArgs(t *testing.T) {
	storage := t.TempDir()
	guestRoot := "/Users/x/proj"
	require.NoError(t, os.WriteFile(filepath.Join(storage, "img.png"), []byte("x"), 0o644))
	registry := []WorkspaceEntry{{ProjectName: "p", GuestPath: guestRoot, StoragePath: storage}}

	withPopExecSeam(t, func(recs *[]popExecRecord) {
		body, _ := json.Marshal(map[string]any{
			"arg":       "img.png",
			"cwd":       guestRoot,
			"open_args": []string{"-a", "Preview"},
		})
		req := httptest.NewRequest(http.MethodPost, "/pop", bytes.NewReader(body))
		w := httptest.NewRecorder()
		handlePop(w, req, "p", registry)

		assert.Equal(t, 200, w.Code, "body: %s", w.Body.String())
		require.Len(t, *recs, 1)
		assert.Equal(t, []string{guestRoot + "/.vm/img.png", "-a", "Preview"}, (*recs)[0].args)
	})
}

func TestPopHandler_AbsoluteGuestArg(t *testing.T) {
	storage := t.TempDir()
	guestRoot := "/Users/x/proj"
	require.NoError(t, os.WriteFile(filepath.Join(storage, "abs.png"), []byte("x"), 0o644))
	registry := []WorkspaceEntry{{ProjectName: "p", GuestPath: guestRoot, StoragePath: storage}}

	withPopExecSeam(t, func(recs *[]popExecRecord) {
		body, _ := json.Marshal(map[string]any{
			"arg": guestRoot + "/abs.png",
			"cwd": guestRoot + "/src", // cwd irrelevant when arg is absolute
		})
		req := httptest.NewRequest(http.MethodPost, "/pop", bytes.NewReader(body))
		w := httptest.NewRecorder()
		handlePop(w, req, "p", registry)

		assert.Equal(t, 200, w.Code)
		require.Len(t, *recs, 1)
		assert.Equal(t, guestRoot+"/.vm/abs.png", (*recs)[0].args[0])
	})
}

// popExecOpen is only referenced inside pop.go for the real impl and
// re-used here in tests via the seam. Suppress unused-import lint
// signal.
var _ = exec.Command
var _ = strings.Contains
