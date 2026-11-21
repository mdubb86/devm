package serviceapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildPassthroughHandler mirrors buildProposeHandler for the guest-
// facing POST /passthrough endpoint. Returns the handler, the
// identity.Config under test, and the *StateCache so tests can seed
// mac cwd + read back pending-passthrough.json.
func buildPassthroughHandler(t *testing.T) (http.Handler, identity.Config, *StateCache) {
	t.Helper()
	cfg := identity.Prod
	t.Setenv("HOME", t.TempDir())
	cache := NewStateCache()
	h := handlePassthroughRequestForProject(cfg, cache, "proj")
	return h, cfg, cache
}

func postPassthrough(h http.Handler, req map[string]any) *httptest.ResponseRecorder {
	body, _ := json.Marshal(req)
	httpReq := httptest.NewRequest(http.MethodPost, "/passthrough", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httpReq)
	return rr
}

func TestPassthroughReq_MissingReasonReturns400(t *testing.T) {
	h, _, cache := buildPassthroughHandler(t)
	writeMacCwdFile(t, cache, "devm.yaml", "project:\n  name: myproj\n")

	rr := postPassthrough(h, map[string]any{"source": "guest"})
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "reason required")
}

func TestPassthroughReq_ProjectNotStartedReturns412(t *testing.T) {
	h, _, _ := buildPassthroughHandler(t)
	// No cache.SetMacCwd — project not started.
	rr := postPassthrough(h, map[string]any{
		"reason":           "need to curl a thing",
		"duration_seconds": 300,
	})
	assert.Equal(t, http.StatusPreconditionFailed, rr.Code)
	assert.Contains(t, rr.Body.String(), "not started")
}

func TestPassthroughReq_GateOffReturns403(t *testing.T) {
	h, _, cache := buildPassthroughHandler(t)
	writeMacCwdFile(t, cache, "devm.yaml",
		"project:\n  name: myproj\nguest:\n  passthrough: false\n")

	rr := postPassthrough(h, map[string]any{
		"reason":           "need to curl a thing",
		"duration_seconds": 300,
	})
	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Contains(t, rr.Body.String(), "guest.passthrough is disabled")
}

// TestPassthroughReq_MissingDurationReturns400 pins the duration-
// required contract at the server boundary: even if a stale gdevm
// binary skips the CLI-side validation, the daemon refuses to record
// a pending with duration_seconds <= 0. The earlier 30s default was
// the source of the "I ran approve, nothing opened" report — window
// had expired by the time the operator got to test.
func TestPassthroughReq_MissingDurationReturns400(t *testing.T) {
	h, _, cache := buildPassthroughHandler(t)
	writeMacCwdFile(t, cache, "devm.yaml", "project:\n  name: myproj\n")

	rr := postPassthrough(h, map[string]any{"reason": "no duration set"})
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "duration required")
}

func TestPassthroughReq_HappyPathWritesPending(t *testing.T) {
	h, cfg, cache := buildPassthroughHandler(t)
	writeMacCwdFile(t, cache, "devm.yaml", "project:\n  name: myproj\n")

	rr := postPassthrough(h, map[string]any{
		"reason":           "need to curl an unlisted mirror",
		"duration_seconds": 300,
		"cwd":              "/home/devm/proj",
		"branch":           "feature-x",
	})
	assert.Equal(t, http.StatusAccepted, rr.Code)
	assert.Contains(t, rr.Body.String(), "awaiting Mac-side approval")

	pending, ok, err := ReadPendingPassthrough(cfg, "proj")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "need to curl an unlisted mirror", pending.Reason)
	assert.Equal(t, 300, pending.DurationSeconds)
	assert.Equal(t, "/home/devm/proj", pending.Cwd)
	assert.Equal(t, "feature-x", pending.Branch)
	assert.Equal(t, "guest", pending.Source)
}

func TestPassthroughReq_SecondRequestReplacesFirst(t *testing.T) {
	h, cfg, cache := buildPassthroughHandler(t)
	writeMacCwdFile(t, cache, "devm.yaml", "project:\n  name: myproj\n")

	rr := postPassthrough(h, map[string]any{"reason": "first", "duration_seconds": 60})
	require.Equal(t, http.StatusAccepted, rr.Code)
	rr = postPassthrough(h, map[string]any{"reason": "second", "duration_seconds": 60})
	require.Equal(t, http.StatusAccepted, rr.Code)

	pending, ok, err := ReadPendingPassthrough(cfg, "proj")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "second", pending.Reason, "most-recent request wins")
}

func TestPendingPassthrough_ClearIdempotent(t *testing.T) {
	cfg := identity.Prod
	t.Setenv("HOME", t.TempDir())

	// Clearing a missing file must not error.
	require.NoError(t, ClearPendingPassthrough(cfg, "proj"))

	require.NoError(t, WritePendingPassthrough(cfg, "proj", PendingPassthroughRequest{
		Reason: "x", Timestamp: "2026-09-28T12:00:00Z", Source: "guest",
	}))
	// File exists now.
	_, err := os.Stat(filepath.Join(cfg.RuntimeDir(), "proj", "pending-passthrough.json"))
	require.NoError(t, err)

	require.NoError(t, ClearPendingPassthrough(cfg, "proj"))
	_, err = os.Stat(filepath.Join(cfg.RuntimeDir(), "proj", "pending-passthrough.json"))
	assert.True(t, os.IsNotExist(err))

	// Second clear is a no-op.
	require.NoError(t, ClearPendingPassthrough(cfg, "proj"))
}
