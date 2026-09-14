package serviceapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildProposeHandler seeds a StateCache with a project row whose
// MacCwd points at macCwd. Returned handler is ready to serve.
func buildProposeHandler(t *testing.T, macCwd string) (http.Handler, identity.Config, *StateCache) {
	t.Helper()
	cfg := identity.Prod
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	require.NoError(t, os.MkdirAll(macCwd, 0o755))
	cache := NewStateCache()
	cache.SetMacCwd("proj", macCwd)
	h := handleProposeForProject(cfg, "proj", cache)
	return h, cfg, cache
}

func postPropose(h http.Handler, req map[string]any) *httptest.ResponseRecorder {
	body, _ := json.Marshal(req)
	httpReq := httptest.NewRequest(http.MethodPost, "/propose", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httpReq)
	return rr
}

func TestPropose_ValidYAMLWritesFileAndMetadata(t *testing.T) {
	macCwd := filepath.Join(t.TempDir(), "proj")
	h, cfg, _ := buildProposeHandler(t, macCwd)

	yamlBody := []byte("project:\n  name: myproj\n")
	rr := postPropose(h, map[string]any{
		"yaml":   base64.StdEncoding.EncodeToString(yamlBody),
		"cwd":    "/home/devm/proj/backend",
		"branch": "feature-x",
		"reason": "adding foo",
		"kind":   "devm.yaml",
	})

	require.Equal(t, http.StatusNoContent, rr.Code, "body: %s", rr.Body.String())

	// File written verbatim.
	written, err := os.ReadFile(filepath.Join(macCwd, "devm.yaml"))
	require.NoError(t, err)
	assert.Equal(t, yamlBody, written)

	// Metadata recorded.
	meta, ok, err := ReadLastProposal(cfg, "proj")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "/home/devm/proj/backend", meta.Cwd)
	assert.Equal(t, "feature-x", meta.Branch)
	assert.Equal(t, "adding foo", meta.Reason)
	assert.Equal(t, "guest", meta.Source)
	assert.Equal(t, "devm.yaml", meta.Kind)
	assert.NotEmpty(t, meta.Timestamp)
}

func TestPropose_InvalidYAMLReturns400AndWritesNothing(t *testing.T) {
	macCwd := filepath.Join(t.TempDir(), "proj")
	h, cfg, _ := buildProposeHandler(t, macCwd)

	// Invalid YAML: unclosed flow mapping.
	yamlBody := []byte("project:\n  name: myproj\nnetwork: {unclosed\n")
	rr := postPropose(h, map[string]any{
		"yaml":   base64.StdEncoding.EncodeToString(yamlBody),
		"cwd":    "/home/devm/proj",
		"branch": "main",
		"reason": "",
		"kind":   "devm.yaml",
	})

	assert.Equal(t, http.StatusBadRequest, rr.Code)

	// No file written.
	_, err := os.Stat(filepath.Join(macCwd, "devm.yaml"))
	assert.True(t, os.IsNotExist(err), "devm.yaml must not exist")

	// No metadata written.
	_, ok, _ := ReadLastProposal(cfg, "proj")
	assert.False(t, ok)
}

func TestPropose_UnsupportedKindReturns400(t *testing.T) {
	macCwd := filepath.Join(t.TempDir(), "proj")
	h, _, _ := buildProposeHandler(t, macCwd)

	rr := postPropose(h, map[string]any{
		"yaml":   base64.StdEncoding.EncodeToString([]byte("project:\n  name: p\n")),
		"cwd":    "/x",
		"branch": "",
		"reason": "",
		"kind":   "devm.me.yaml",
	})

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "kind")
}

func TestPropose_InvalidBase64Returns400(t *testing.T) {
	macCwd := filepath.Join(t.TempDir(), "proj")
	h, _, _ := buildProposeHandler(t, macCwd)

	rr := postPropose(h, map[string]any{
		"yaml":   "!!not-base64!!",
		"cwd":    "/x",
		"branch": "",
		"reason": "",
		"kind":   "devm.yaml",
	})

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestPropose_MissingMacCwdReturns500(t *testing.T) {
	cfg := identity.Prod
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	cache := NewStateCache()
	// Deliberately do NOT set MacCwd for "proj".
	h := handleProposeForProject(cfg, "proj", cache)

	rr := postPropose(h, map[string]any{
		"yaml":   base64.StdEncoding.EncodeToString([]byte("project:\n  name: p\n")),
		"cwd":    "/x",
		"branch": "",
		"reason": "",
		"kind":   "devm.yaml",
	})

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	assert.Contains(t, strings.ToLower(rr.Body.String()), "mac_cwd")
}

func TestPropose_SecondProposalOverwritesMetadata(t *testing.T) {
	macCwd := filepath.Join(t.TempDir(), "proj")
	h, cfg, _ := buildProposeHandler(t, macCwd)

	post := func(reason string) {
		rr := postPropose(h, map[string]any{
			"yaml":   base64.StdEncoding.EncodeToString([]byte("project:\n  name: p\n")),
			"cwd":    "/x",
			"branch": "",
			"reason": reason,
			"kind":   "devm.yaml",
		})
		require.Equal(t, http.StatusNoContent, rr.Code)
	}

	post("first")
	post("second")

	meta, ok, err := ReadLastProposal(cfg, "proj")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "second", meta.Reason)
}

// TestPropose_ListenerRegisteredOnStart pins that serveProposeListener
// records the listener in proposeListeners and closeProposeListener
// removes it.
func TestPropose_ListenerRegisteredOnStart(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	proposeListeners.Store("proj", ln)
	got, ok := proposeListeners.Load("proj")
	require.True(t, ok)
	assert.Equal(t, ln, got.(net.Listener))

	closeProposeListener("proj")
	_, ok = proposeListeners.Load("proj")
	assert.False(t, ok, "closeProposeListener must delete entry")
}
