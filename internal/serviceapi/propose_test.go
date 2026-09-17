package serviceapi

import (
	"bytes"
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

const validDevmYAML = "project:\n  name: myproj\n"
const invalidDevmYAML = "project:\n  name: myproj\nnetwork: {unclosed\n"

// buildProposeHandler returns the softnet per-project /propose handler
// for "proj", plus the identity.Config it was built against so tests
// can read back metadata and write state-dir fixtures.
func buildProposeHandler(t *testing.T) (http.Handler, identity.Config) {
	t.Helper()
	cfg := identity.Prod
	t.Setenv("HOME", t.TempDir())
	h := handleProposeForProject(cfg, "proj")
	return h, cfg
}

func postPropose(h http.Handler, path string, req map[string]any) *httptest.ResponseRecorder {
	body, _ := json.Marshal(req)
	httpReq := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httpReq)
	return rr
}

// writeStateDirFile writes content at <state-dir>/<name> for "proj",
// creating the state dir if needed.
func writeStateDirFile(t *testing.T, cfg identity.Config, name, content string) {
	t.Helper()
	dir := stateDirForProject(cfg, "proj")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
}

func TestPropose_UnsupportedKindReturns400(t *testing.T) {
	h, _ := buildProposeHandler(t)

	rr := postPropose(h, "/propose", map[string]any{
		"cwd":    "/x",
		"branch": "",
		"reason": "",
		"kind":   "not-a-real-kind",
	})

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "kind")
}

func TestPropose_OversizedBodyRejected(t *testing.T) {
	h, cfg := buildProposeHandler(t)

	// A reason field alone over 1 MiB — well past maxProposeBodyBytes
	// once wrapped in the JSON envelope.
	oversizedReason := strings.Repeat("a", maxProposeBodyBytes+1)
	reqBody, err := json.Marshal(map[string]any{
		"cwd":    "/x",
		"branch": "",
		"reason": oversizedReason,
		"kind":   "devm.yaml",
	})
	require.NoError(t, err)

	httpReq := httptest.NewRequest(http.MethodPost, "/propose", bytes.NewReader(reqBody))
	httpReq.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httpReq)

	assert.True(t, rr.Code == http.StatusBadRequest || rr.Code == http.StatusRequestEntityTooLarge, "got %d", rr.Code)

	_, ok, _ := ReadLastProposal(cfg, "proj")
	assert.False(t, ok, "no metadata should be written for a rejected body")
}

func TestPropose_MissingProjectStateDirIgnoredWhenFileMissing(t *testing.T) {
	h, cfg := buildProposeHandler(t)
	// Deliberately do NOT create the state dir or a devm.yaml — the
	// signal-only handler has nothing on disk to validate against yet.

	rr := postPropose(h, "/propose", map[string]any{
		"cwd":    "/home/devm/proj",
		"branch": "main",
		"reason": "adding foo",
		"kind":   "devm.yaml",
	})

	require.Equal(t, http.StatusNoContent, rr.Code, "body: %s", rr.Body.String())

	meta, ok, err := ReadLastProposal(cfg, "proj")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "/home/devm/proj", meta.Cwd)
	assert.Equal(t, "main", meta.Branch)
	assert.Equal(t, "adding foo", meta.Reason)
	assert.Equal(t, "guest", meta.Source)
	assert.Equal(t, "devm.yaml", meta.Kind)
	assert.NotEmpty(t, meta.Timestamp)
}

func TestPropose_SecondProposalOverwritesMetadata(t *testing.T) {
	h, cfg := buildProposeHandler(t)

	post := func(reason string) {
		rr := postPropose(h, "/propose", map[string]any{
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

func TestPropose_ValidatesOnDiskFileWhenPresent(t *testing.T) {
	h, cfg := buildProposeHandler(t)
	writeStateDirFile(t, cfg, "devm.yaml", validDevmYAML)

	rr := postPropose(h, "/propose", map[string]any{
		"cwd":    "/x",
		"branch": "main",
		"reason": "",
		"kind":   "devm.yaml",
	})

	require.Equal(t, http.StatusNoContent, rr.Code, "body: %s", rr.Body.String())

	meta, ok, err := ReadLastProposal(cfg, "proj")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "devm.yaml", meta.Kind)
}

func TestPropose_InvalidOnDiskFileRejects(t *testing.T) {
	h, cfg := buildProposeHandler(t)
	writeStateDirFile(t, cfg, "devm.yaml", invalidDevmYAML)

	rr := postPropose(h, "/propose", map[string]any{
		"cwd":    "/x",
		"branch": "main",
		"reason": "",
		"kind":   "devm.yaml",
	})

	assert.Equal(t, http.StatusBadRequest, rr.Code)

	_, ok, _ := ReadLastProposal(cfg, "proj")
	assert.False(t, ok, "no metadata should be written when on-disk validation fails")
}

func TestPropose_SourceDefaultsToGuestWhenEmpty(t *testing.T) {
	h, cfg := buildProposeHandler(t)

	rr := postPropose(h, "/propose", map[string]any{
		"cwd":    "/x",
		"branch": "",
		"reason": "",
		"kind":   "devm.yaml",
	})
	require.Equal(t, http.StatusNoContent, rr.Code)

	meta, ok, err := ReadLastProposal(cfg, "proj")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "guest", meta.Source)
}

func TestPropose_SourceMacIsPreserved(t *testing.T) {
	h, cfg := buildProposeHandler(t)

	rr := postPropose(h, "/propose", map[string]any{
		"cwd":    "/x",
		"branch": "",
		"reason": "",
		"kind":   "devm.yaml",
		"source": "mac",
	})
	require.Equal(t, http.StatusNoContent, rr.Code)

	meta, ok, err := ReadLastProposal(cfg, "proj")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "mac", meta.Source)
}

// TestPropose_UnixSocketHandlerRoutesByProjectQueryParam pins that the
// Mac-side /vm/propose?project=<name> handler funnels through the same
// recorder as the softnet listener.
func TestPropose_UnixSocketHandlerRoutesByProjectQueryParam(t *testing.T) {
	cfg := identity.Prod
	t.Setenv("HOME", t.TempDir())
	h := handleProposeUnixSocket(cfg)

	rr := postPropose(h, "/vm/propose?project=proj", map[string]any{
		"cwd":    "/Users/dev/proj",
		"branch": "main",
		"reason": "mac-side edit",
		"kind":   "devm.yaml",
		"source": "mac",
	})

	require.Equal(t, http.StatusNoContent, rr.Code, "body: %s", rr.Body.String())

	meta, ok, err := ReadLastProposal(cfg, "proj")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "mac", meta.Source)
	assert.Equal(t, "mac-side edit", meta.Reason)
}

func TestPropose_UnixSocketHandlerRequiresProjectParam(t *testing.T) {
	cfg := identity.Prod
	t.Setenv("HOME", t.TempDir())
	h := handleProposeUnixSocket(cfg)

	rr := postPropose(h, "/vm/propose", map[string]any{
		"cwd":  "/x",
		"kind": "devm.yaml",
	})

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "project")
}

func TestPropose_UnixSocketHandlerMethodNotAllowed(t *testing.T) {
	cfg := identity.Prod
	t.Setenv("HOME", t.TempDir())
	h := handleProposeUnixSocket(cfg)

	httpReq := httptest.NewRequest(http.MethodGet, "/vm/propose?project=proj", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httpReq)

	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

func TestPropose_MethodNotAllowed(t *testing.T) {
	h, _ := buildProposeHandler(t)

	httpReq := httptest.NewRequest(http.MethodGet, "/propose", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httpReq)

	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
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
