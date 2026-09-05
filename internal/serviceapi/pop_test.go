package serviceapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestPopHandler_ReturnsMirrorPathDirectly(t *testing.T) {
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
		got := (*recs)[0].args[0]
		assert.Equal(t, filepath.Join(storage, "src", "foo.png"), got,
			"open must receive the Mac mirror path directly")
		assert.NotContains(t, got, ".vm/", "no .vm/-suffixed indirection")
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
		assert.Equal(t, filepath.Join(storage, "top.png"), (*recs)[0].args[0])
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
		assert.Equal(t, []string{filepath.Join(storage, "img.png"), "-a", "Preview"}, (*recs)[0].args)
	})
}

func TestPopHandler_URL_PassedStraightToOpen(t *testing.T) {
	storage := t.TempDir()
	guestRoot := "/Users/x/proj"
	registry := []WorkspaceEntry{{ProjectName: "p", GuestPath: guestRoot, StoragePath: storage}}

	withPopExecSeam(t, func(recs *[]popExecRecord) {
		body, _ := json.Marshal(map[string]any{
			"arg": "https://example.com/thing",
			"cwd": guestRoot + "/src",
		})
		req := httptest.NewRequest(http.MethodPost, "/pop", bytes.NewReader(body))
		w := httptest.NewRecorder()
		handlePop(w, req, "p", registry)

		assert.Equal(t, 200, w.Code, "body: %s", w.Body.String())
		require.Len(t, *recs, 1)
		require.Len(t, (*recs)[0].args, 1)
		assert.Equal(t, "https://example.com/thing", (*recs)[0].args[0],
			"URL must be passed to `open` verbatim, not translated as a path")
	})
}

func TestPopHandler_URL_ForwardsOpenArgs(t *testing.T) {
	storage := t.TempDir()
	guestRoot := "/Users/x/proj"
	registry := []WorkspaceEntry{{ProjectName: "p", GuestPath: guestRoot, StoragePath: storage}}

	withPopExecSeam(t, func(recs *[]popExecRecord) {
		body, _ := json.Marshal(map[string]any{
			"arg":       "http://localhost:3000",
			"cwd":       guestRoot,
			"open_args": []string{"-a", "Firefox"},
		})
		req := httptest.NewRequest(http.MethodPost, "/pop", bytes.NewReader(body))
		w := httptest.NewRecorder()
		handlePop(w, req, "p", registry)

		assert.Equal(t, 200, w.Code, "body: %s", w.Body.String())
		require.Len(t, *recs, 1)
		assert.Equal(t, []string{"http://localhost:3000", "-a", "Firefox"}, (*recs)[0].args)
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
		assert.Equal(t, filepath.Join(storage, "abs.png"), (*recs)[0].args[0])
	})
}

func TestPopHandler_NotInMirror_FileKind_CreatesSessionOpensMacTarget(t *testing.T) {
	cfg := testPopSessionCfg(t)

	scripted := &scriptedCLI{}
	cli := scripted.build()
	store := NewPopSessionStore()

	registry := []WorkspaceEntry{{ProjectName: "p", GuestPath: "/home/devm/proj", StoragePath: t.TempDir()}}

	withPopExecSeam(t, func(recs *[]popExecRecord) {
		body, _ := json.Marshal(map[string]any{
			"arg":           "/tmp/site/index.html",
			"cwd":           "/home/devm/proj",
			"resolved_path": "/tmp/site/index.html",
			"is_dir":        false,
		})
		req := httptest.NewRequest(http.MethodPost, "/pop", bytes.NewReader(body))
		w := httptest.NewRecorder()
		handlePopWithDeps(w, req, "p", registry, cfg, store, cli, "devm-p")

		assert.Equal(t, 200, w.Code, "body: %s", w.Body.String())
		require.Len(t, *recs, 1)
		got := (*recs)[0].args[0]
		// For a file-scope session, open target is MacDir/<basename>.
		session, ok := store.Get("/tmp/site/index.html")
		require.True(t, ok)
		assert.Equal(t, filepath.Join(session.MacDir, "index.html"), got)
	})
}

func TestPopHandler_NotInMirror_DirKind_OpensMacDir(t *testing.T) {
	cfg := testPopSessionCfg(t)

	scripted := &scriptedCLI{}
	cli := scripted.build()
	store := NewPopSessionStore()

	registry := []WorkspaceEntry{{ProjectName: "p", GuestPath: "/home/devm/proj", StoragePath: t.TempDir()}}

	withPopExecSeam(t, func(recs *[]popExecRecord) {
		body, _ := json.Marshal(map[string]any{
			"arg":           "/tmp/site",
			"cwd":           "/home/devm/proj",
			"resolved_path": "/tmp/site",
			"is_dir":        true,
		})
		req := httptest.NewRequest(http.MethodPost, "/pop", bytes.NewReader(body))
		w := httptest.NewRecorder()
		handlePopWithDeps(w, req, "p", registry, cfg, store, cli, "devm-p")

		assert.Equal(t, 200, w.Code, "body: %s", w.Body.String())
		require.Len(t, *recs, 1)
		session, ok := store.Get("/tmp/site")
		require.True(t, ok)
		assert.Equal(t, session.MacDir, (*recs)[0].args[0], "dir-scope opens MacDir itself")
	})
}

func TestPopHandler_NotInMirror_NoResolvedPath_Returns404(t *testing.T) {
	cfg := testPopSessionCfg(t)

	scripted := &scriptedCLI{}
	cli := scripted.build()
	store := NewPopSessionStore()

	registry := []WorkspaceEntry{{ProjectName: "p", GuestPath: "/home/devm/proj", StoragePath: t.TempDir()}}

	body, _ := json.Marshal(map[string]any{
		"arg": "nothing-here.txt",
		"cwd": "/home/devm/proj",
		// no resolved_path — stat failed guest-side
	})
	req := httptest.NewRequest(http.MethodPost, "/pop", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handlePopWithDeps(w, req, "p", registry, cfg, store, cli, "devm-p")

	assert.Equal(t, 404, w.Code)
	assert.Contains(t, w.Body.String(), "no such file")
}
