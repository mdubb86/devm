package serviceapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPopSessionEndpoint_FileKind_ReturnsMacDirTarget(t *testing.T) {
	cfg := testPopSessionCfg(t)
	scripted := &scriptedCLI{}
	cli := scripted.build()
	store := NewPopSessionStore()

	// The endpoint trusts the caller's guest_path verbatim — no stat.
	guestPath := filepath.Join(t.TempDir(), "guest-tree", "site", "index.html")

	resolveSSH := func(project string) string { return "devm-" + project }
	handler := popSessionHandler(cfg, store, cli, resolveSSH)

	body, _ := json.Marshal(map[string]any{
		"project":    "p",
		"guest_path": guestPath,
		"is_dir":     false,
	})
	req := httptest.NewRequest(http.MethodPost, "/pop-session", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handler(w, req)

	require.Equal(t, 200, w.Code, "body: %s", w.Body.String())
	var resp struct {
		MacPath string `json:"mac_path"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	session, ok := store.Get(guestPath)
	require.True(t, ok)
	assert.Equal(t, filepath.Join(session.MacDir, "index.html"), resp.MacPath)
}

func TestPopSessionEndpoint_DirKind_ReturnsMacDirItself(t *testing.T) {
	cfg := testPopSessionCfg(t)
	scripted := &scriptedCLI{}
	cli := scripted.build()
	store := NewPopSessionStore()

	guestPath := filepath.Join(t.TempDir(), "guest-tree", "site")

	resolveSSH := func(project string) string { return "devm-" + project }
	handler := popSessionHandler(cfg, store, cli, resolveSSH)

	body, _ := json.Marshal(map[string]any{
		"project": "p", "guest_path": guestPath, "is_dir": true,
	})
	req := httptest.NewRequest(http.MethodPost, "/pop-session", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handler(w, req)

	require.Equal(t, 200, w.Code, "body: %s", w.Body.String())
	var resp struct {
		MacPath string `json:"mac_path"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	session, ok := store.Get(guestPath)
	require.True(t, ok)
	assert.Equal(t, session.MacDir, resp.MacPath)
}

func TestPopSessionEndpoint_MissingProject_400(t *testing.T) {
	cfg := testPopSessionCfg(t)
	scripted := &scriptedCLI{}
	cli := scripted.build()
	store := NewPopSessionStore()
	handler := popSessionHandler(cfg, store, cli, func(string) string { return "devm-x" })

	body, _ := json.Marshal(map[string]any{"guest_path": "/tmp/x"})
	req := httptest.NewRequest(http.MethodPost, "/pop-session", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handler(w, req)
	assert.Equal(t, 400, w.Code)
}

func TestPopSessionEndpoint_MissingGuestPath_400(t *testing.T) {
	cfg := testPopSessionCfg(t)
	scripted := &scriptedCLI{}
	cli := scripted.build()
	store := NewPopSessionStore()
	handler := popSessionHandler(cfg, store, cli, func(string) string { return "devm-x" })

	body, _ := json.Marshal(map[string]any{"project": "p"})
	req := httptest.NewRequest(http.MethodPost, "/pop-session", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handler(w, req)
	assert.Equal(t, 400, w.Code)
}

func TestPopSessionEndpoint_RelativeGuestPath_400(t *testing.T) {
	cfg := testPopSessionCfg(t)
	scripted := &scriptedCLI{}
	cli := scripted.build()
	store := NewPopSessionStore()
	handler := popSessionHandler(cfg, store, cli, func(string) string { return "devm-x" })

	body, _ := json.Marshal(map[string]any{"project": "p", "guest_path": "relative/path"})
	req := httptest.NewRequest(http.MethodPost, "/pop-session", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handler(w, req)
	assert.Equal(t, 400, w.Code)
}

func TestPopSessionEndpoint_UnknownProject_404(t *testing.T) {
	cfg := testPopSessionCfg(t)
	scripted := &scriptedCLI{}
	cli := scripted.build()
	store := NewPopSessionStore()
	handler := popSessionHandler(cfg, store, cli, func(string) string { return "" })

	body, _ := json.Marshal(map[string]any{"project": "p", "guest_path": "/tmp/x"})
	req := httptest.NewRequest(http.MethodPost, "/pop-session", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handler(w, req)
	assert.Equal(t, 404, w.Code)
}

func TestPopSessionEndpoint_NonPost_405(t *testing.T) {
	cfg := testPopSessionCfg(t)
	scripted := &scriptedCLI{}
	cli := scripted.build()
	store := NewPopSessionStore()
	handler := popSessionHandler(cfg, store, cli, func(string) string { return "devm-x" })

	req := httptest.NewRequest(http.MethodGet, "/pop-session", nil)
	w := httptest.NewRecorder()
	handler(w, req)
	assert.Equal(t, 405, w.Code)
}

func TestRegisterPopSessionHandler_InstallsRoute(t *testing.T) {
	cfg := testPopSessionCfg(t)
	scripted := &scriptedCLI{}
	cli := scripted.build()
	store := NewPopSessionStore()

	dir, err := os.MkdirTemp("/tmp", "sapi-pop-session-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	srv := NewServer(filepath.Join(dir, "s.sock"), Build{Version: "test-version"})

	RegisterPopSessionHandler(srv, cfg, store, cli, func(string) string { return "devm-p" })

	body, _ := json.Marshal(map[string]any{
		"project": "p", "guest_path": "/tmp/registered", "is_dir": true,
	})
	req := httptest.NewRequest(http.MethodPost, "/pop-session", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	assert.Equal(t, 200, w.Code, "body: %s", w.Body.String())
}
