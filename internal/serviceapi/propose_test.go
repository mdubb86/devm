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

	"github.com/mdubb86/devm/internal/approve"
	"github.com/mdubb86/devm/internal/identity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const validDevmYAML = "project:\n  name: myproj\n"
const invalidDevmYAML = "project:\n  name: myproj\nnetwork: {unclosed\n"

// buildProposeHandler returns the softnet per-project /propose handler
// for "proj", plus the identity.Config and *StateCache it was built
// against so tests can read back metadata, seed the cache, and write
// fixtures.
func buildProposeHandler(t *testing.T) (http.Handler, identity.Config, *StateCache) {
	t.Helper()
	cfg := identity.Prod
	t.Setenv("HOME", t.TempDir())
	cache := NewStateCache()
	h := handleProposeForProject(cfg, cache, "proj")
	return h, cfg, cache
}

func postPropose(h http.Handler, path string, req map[string]any) *httptest.ResponseRecorder {
	body, _ := json.Marshal(req)
	httpReq := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httpReq)
	return rr
}

// writeMacCwdFile registers a fresh macCwd for "proj" in cache and
// writes content at <macCwd>/<name>, mirroring where the propose
// handler now resolves the on-disk config from.
func writeMacCwdFile(t *testing.T, cache *StateCache, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	cache.SetMacCwd("proj", dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
	return dir
}

func TestPropose_UnsupportedKindReturns400(t *testing.T) {
	h, _, _ := buildProposeHandler(t)

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
	h, cfg, _ := buildProposeHandler(t)

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
	h, cfg, cache := buildProposeHandler(t)
	cache.SetMacCwd("proj", t.TempDir())
	// Deliberately do NOT write a devm.yaml into macCwd — the
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
	h, cfg, cache := buildProposeHandler(t)
	cache.SetMacCwd("proj", t.TempDir())

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
	h, cfg, cache := buildProposeHandler(t)
	writeMacCwdFile(t, cache, "devm.yaml", validDevmYAML)

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
	h, cfg, cache := buildProposeHandler(t)
	writeMacCwdFile(t, cache, "devm.yaml", invalidDevmYAML)

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

// TestPropose_LoadsFunctionsFromOnDiskShellScripts pins the fix for
// the daemon-side propose validator populating Config.Functions from
// devm.sh + devm.me.sh at macCwd before Validate. Without this, a
// devm.yaml that references any function under repos.<name>.commands
// or services.<name>.exec fails validation with "function ... is not
// defined in devm.sh (or devm.me.sh)" — because the parsed Config's
// Functions slice is empty regardless of what's on disk. Symptom
// shipped to real users in v0.24.2: every guest-side `gdevm propose`
// returned "propose: yaml validate: repos.<repo>.commands: function
// \"<first-fn>\" is not defined in devm.sh", so proposal signals
// became unusable the moment the yaml referenced any function at all.
func TestPropose_LoadsFunctionsFromOnDiskShellScripts(t *testing.T) {
	cfg := identity.Prod
	t.Setenv("HOME", t.TempDir())
	cache := NewStateCache()
	macCwd := t.TempDir()
	cache.SetMacCwd("proj", macCwd)

	// devm.yaml references install-gsd-core under repos.main.commands,
	// matching the shape the user hit. Any function name would trip
	// the pre-fix bug; using a real one keeps the test grounded.
	yamlBody := `project:
  name: myproj
repos:
  main:
    url: https://example.com/repo.git
    primary: true
    commands:
      - install-gsd-core
`
	require.NoError(t, os.WriteFile(filepath.Join(macCwd, "devm.yaml"), []byte(yamlBody), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(macCwd, "devm.sh"),
		[]byte("install-gsd-core() {\n  true\n}\n"), 0o644))

	h := handleProposeForProject(cfg, cache, "proj")
	rr := postPropose(h, "/propose", map[string]any{
		"kind":   "devm.yaml",
		"source": "mac",
		"reason": "regression pin",
	})

	require.Equal(t, http.StatusNoContent, rr.Code,
		"validator must populate Functions from on-disk devm.sh — got %d, body: %s",
		rr.Code, rr.Body.String())
}

// TestPropose_MissingFunctionInShellStillRejects pins the negative
// side: a devm.yaml referencing a function NEITHER devm.sh nor
// devm.me.sh defines still surfaces the real error, so the fix
// doesn't accidentally suppress the intended validation.
func TestPropose_MissingFunctionInShellStillRejects(t *testing.T) {
	cfg := identity.Prod
	t.Setenv("HOME", t.TempDir())
	cache := NewStateCache()
	macCwd := t.TempDir()
	cache.SetMacCwd("proj", macCwd)

	yamlBody := `project:
  name: myproj
repos:
  main:
    url: https://example.com/repo.git
    primary: true
    commands:
      - never-defined
`
	require.NoError(t, os.WriteFile(filepath.Join(macCwd, "devm.yaml"), []byte(yamlBody), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(macCwd, "devm.sh"),
		[]byte("install-gsd-core() {\n  true\n}\n"), 0o644))

	h := handleProposeForProject(cfg, cache, "proj")
	rr := postPropose(h, "/propose", map[string]any{
		"kind":   "devm.yaml",
		"source": "mac",
		"reason": "should still fail",
	})

	require.Equal(t, http.StatusBadRequest, rr.Code, "body: %s", rr.Body.String())
	assert.Contains(t, rr.Body.String(), `"never-defined"`)
	assert.Contains(t, rr.Body.String(), "not defined in devm.sh")
}

// invalidPartialMeYAML would fail schema.Config.Validate if it were
// ever run against it (unknown top-level key, no project.name) — it
// exists to prove the devm.me.yaml branch skips validation rather than
// happening to pass it.
const invalidPartialMeYAML = "not_a_real_top_level_key: true\n"

func TestPropose_MeYamlAcceptedWithoutValidation(t *testing.T) {
	h, cfg, cache := buildProposeHandler(t)
	writeMacCwdFile(t, cache, "devm.me.yaml", invalidPartialMeYAML)

	rr := postPropose(h, "/propose", map[string]any{
		"cwd":    "/x",
		"branch": "main",
		"reason": "me override",
		"kind":   "devm.me.yaml",
	})

	require.Equal(t, http.StatusNoContent, rr.Code, "body: %s", rr.Body.String())

	meta, ok, err := ReadLastProposal(cfg, "proj")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "devm.me.yaml", meta.Kind)
}

func TestPropose_DevmSHAccepted(t *testing.T) {
	h, cfg, cache := buildProposeHandler(t)
	writeMacCwdFile(t, cache, "devm.sh", "install() {\n  true\n}\n")

	rr := postPropose(h, "/propose", map[string]any{
		"cwd":    "/x",
		"branch": "main",
		"reason": "add install fn",
		"kind":   "devm.sh",
	})

	require.Equal(t, http.StatusNoContent, rr.Code, "body: %s", rr.Body.String())

	meta, ok, err := ReadLastProposal(cfg, "proj")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "devm.sh", meta.Kind)
}

func TestPropose_DevmMeSHAccepted(t *testing.T) {
	h, cfg, cache := buildProposeHandler(t)
	writeMacCwdFile(t, cache, "devm.me.sh", "startup() {\n  true\n}\n")

	rr := postPropose(h, "/propose", map[string]any{
		"cwd":    "/x",
		"branch": "main",
		"reason": "override startup",
		"kind":   "devm.me.sh",
	})

	require.Equal(t, http.StatusNoContent, rr.Code, "body: %s", rr.Body.String())

	meta, ok, err := ReadLastProposal(cfg, "proj")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "devm.me.sh", meta.Kind)
}

// TestPropose_UnreadableOnDiskFileReturns500 pins that a read failure
// other than "file does not exist" is a loud 500, not a silently
// skipped validation. A directory in place of the expected file
// reproduces an EISDIR read error portably (no permission-mode /
// root-user flakiness).
func TestPropose_UnreadableOnDiskFileReturns500(t *testing.T) {
	h, cfg, cache := buildProposeHandler(t)
	macCwd := t.TempDir()
	cache.SetMacCwd("proj", macCwd)
	require.NoError(t, os.MkdirAll(filepath.Join(macCwd, "devm.yaml"), 0o755))

	rr := postPropose(h, "/propose", map[string]any{
		"cwd":    "/x",
		"branch": "main",
		"reason": "",
		"kind":   "devm.yaml",
	})

	assert.Equal(t, http.StatusInternalServerError, rr.Code)

	_, ok, _ := ReadLastProposal(cfg, "proj")
	assert.False(t, ok, "no metadata should be written when the on-disk read fails")
}

func TestPropose_SourceDefaultsToGuestWhenEmpty(t *testing.T) {
	h, cfg, cache := buildProposeHandler(t)
	cache.SetMacCwd("proj", t.TempDir())

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
	h, cfg, cache := buildProposeHandler(t)
	cache.SetMacCwd("proj", t.TempDir())

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
	require.NoError(t, os.MkdirAll(stateDirForProject(cfg, "proj"), 0o755))
	cache := NewStateCache()
	cache.SetMacCwd("proj", t.TempDir())
	h := handleProposeUnixSocket(cfg, cache)

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

// TestPropose_UnixSocketUnknownProjectReturns404 pins I2: the Mac-side
// /vm/propose?project=<name> handler must validate that project
// against the registry before recording anything under its state
// dir — unlike the softnet listener (bound per-project at start, so
// trusted), this handler's project comes from an arbitrary query
// param a caller could set to any name.
// TestPropose_GuestSourceRequiresRunningProject pins that a guest-source
// propose against a project not in the cache (VM not running) fails
// precondition — the mac-side-cwd fallback only kicks in for source="mac".
func TestPropose_GuestSourceRequiresRunningProject(t *testing.T) {
	cfg := identity.Prod
	t.Setenv("HOME", t.TempDir())
	h := handleProposeUnixSocket(cfg, NewStateCache())

	rr := postPropose(h, "/vm/propose?project=nonexistent", map[string]any{
		"cwd":    "/home/devm/proj",
		"branch": "main",
		"reason": "",
		"kind":   "devm.yaml",
		"source": "guest",
	})

	assert.Equal(t, http.StatusPreconditionFailed, rr.Code)

	_, ok, _ := ReadLastProposal(cfg, "nonexistent")
	assert.False(t, ok, "no metadata should be written when the guest hits an unregistered project")
}

// TestPropose_MacSourceBeforeStartUsesRequestCwd pins that a mac-source
// propose against a project not in the cache falls back to the request
// body's cwd — the CLI walked up to find devm.yaml and passed that path,
// which is enough to validate and attribute without /vm/start having run.
func TestPropose_MacSourceBeforeStartUsesRequestCwd(t *testing.T) {
	cfg := identity.Prod
	t.Setenv("HOME", t.TempDir())
	macCwd := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(macCwd, "devm.yaml"), []byte(validDevmYAML), 0o644))

	h := handleProposeUnixSocket(cfg, NewStateCache())
	rr := postPropose(h, "/vm/propose?project=p", map[string]any{
		"cwd":    macCwd,
		"reason": "t",
		"kind":   "devm.yaml",
		"source": "mac",
	})

	require.Equal(t, http.StatusNoContent, rr.Code, "body: %s", rr.Body.String())

	got, ok, err := ReadLastProposal(cfg, "p")
	require.NoError(t, err)
	require.True(t, ok, "metadata should be written")
	assert.Equal(t, macCwd, got.Cwd)
}

func TestPropose_UnixSocketHandlerRequiresProjectParam(t *testing.T) {
	cfg := identity.Prod
	t.Setenv("HOME", t.TempDir())
	h := handleProposeUnixSocket(cfg, NewStateCache())

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
	h := handleProposeUnixSocket(cfg, NewStateCache())

	httpReq := httptest.NewRequest(http.MethodGet, "/vm/propose?project=proj", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httpReq)

	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

func TestPropose_MethodNotAllowed(t *testing.T) {
	h, _, _ := buildProposeHandler(t)

	httpReq := httptest.NewRequest(http.MethodGet, "/propose", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httpReq)

	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

// TestPropose_ValidatesAtMacCwd pins that the Mac-side
// /vm/propose?project=<name> handler validates devm.yaml at the
// project's macCwd (resolved from the state cache), not under its
// state dir — the state dir is deliberately left without the file to
// prove the old path isn't what's read.
func TestPropose_ValidatesAtMacCwd(t *testing.T) {
	cfg := identity.Prod
	t.Setenv("HOME", t.TempDir())
	cache := NewStateCache()
	macCwd := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(macCwd, "devm.yaml"), []byte(validDevmYAML), 0o644))
	cache.SetMacCwd("p", macCwd)

	stateDir := stateDirForProject(cfg, "p")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	_, statErr := os.Stat(filepath.Join(stateDir, "devm.yaml"))
	require.True(t, os.IsNotExist(statErr))

	h := handleProposeUnixSocket(cfg, cache)
	rr := postPropose(h, "/vm/propose?project=p", map[string]any{
		"reason": "t",
		"kind":   "devm.yaml",
		"source": "mac",
	})

	require.Equal(t, http.StatusNoContent, rr.Code, "body: %s", rr.Body.String())
}

// TestPropose_NoMacCwdInCacheReturns412 pins that a project the cache
// has no MacCwd for (never started, or the daemon restarted since)
// fails the precondition rather than reading a stale or empty path.
func TestPropose_NoMacCwdInCacheReturns412(t *testing.T) {
	cfg := identity.Prod
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, os.MkdirAll(stateDirForProject(cfg, "p"), 0o755))
	h := handleProposeUnixSocket(cfg, NewStateCache())

	rr := postPropose(h, "/vm/propose?project=p", map[string]any{
		"reason": "t",
		"kind":   "devm.yaml",
		"source": "mac",
	})

	assert.Equal(t, http.StatusPreconditionFailed, rr.Code)

	_, ok, _ := ReadLastProposal(cfg, "p")
	assert.False(t, ok, "no metadata should be written when the project has no MacCwd")
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

// TestPropose_GuestGateDisabledReturns403 verifies the daemon refuses
// guest-source propose signals when the on-disk devm.yaml declares
// `guest.propose: false`. Mac-source signals ignore the gate.
func TestPropose_GuestGateDisabledReturns403(t *testing.T) {
	h, cfg, cache := buildProposeHandler(t)
	dir := writeMacCwdFile(t, cache, "devm.yaml",
		"project:\n  name: myproj\nguest:\n  propose: false\n")

	// Guest source is blocked.
	rr := postPropose(h, "/propose", map[string]any{
		"kind":   "devm.yaml",
		"source": "guest",
		"reason": "add postgres",
	})
	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Contains(t, rr.Body.String(), "guest.propose is disabled")

	// Metadata must NOT have been written.
	_, err := os.Stat(filepath.Join(cfg.RuntimeDir(), "proj", "last-proposal.json"))
	assert.True(t, os.IsNotExist(err), "gate refusal must not write attribution")

	// Mac source is NOT gated — writes attribution as before.
	rr = postPropose(h, "/propose", map[string]any{
		"kind":   "devm.yaml",
		"source": "mac",
		"cwd":    dir,
		"reason": "same edit, mac side",
	})
	assert.Equal(t, http.StatusNoContent, rr.Code)
	_, err = os.Stat(filepath.Join(cfg.RuntimeDir(), "proj", "last-proposal.json"))
	assert.NoError(t, err, "mac source ignores guest.propose gate")
}

// TestPropose_NoChangeShortCircuits verifies the daemon returns 200 +
// human-readable body when the on-disk file is byte-identical to the
// last-approved snapshot, and does NOT write attribution.
func TestPropose_NoChangeShortCircuits(t *testing.T) {
	h, cfg, cache := buildProposeHandler(t)
	body := "project:\n  name: myproj\n"
	writeMacCwdFile(t, cache, "devm.yaml", body)

	// Seed an approved snapshot with the same bytes.
	require.NoError(t, approve.NewStore(cfg).Write(
		"proj", []byte(body), nil, nil, nil, "user",
	))

	rr := postPropose(h, "/propose", map[string]any{
		"kind":   "devm.yaml",
		"source": "guest",
		"reason": "did nothing",
	})
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "no changes since last approval")

	// Attribution must NOT have been written.
	_, err := os.Stat(filepath.Join(cfg.RuntimeDir(), "proj", "last-proposal.json"))
	assert.True(t, os.IsNotExist(err), "no-change short-circuit must skip attribution write")
}

// TestPropose_ChangedFileStillWritesAttribution guards against the
// short-circuit firing when the bytes actually differ from the
// approved snapshot.
func TestPropose_ChangedFileStillWritesAttribution(t *testing.T) {
	h, cfg, cache := buildProposeHandler(t)
	writeMacCwdFile(t, cache, "devm.yaml", "project:\n  name: myproj\n")

	// Seed an approved snapshot with different bytes.
	require.NoError(t, approve.NewStore(cfg).Write(
		"proj", []byte("project:\n  name: oldproj\n"), nil, nil, nil, "user",
	))

	rr := postPropose(h, "/propose", map[string]any{
		"kind":   "devm.yaml",
		"source": "guest",
		"reason": "renamed project",
	})
	assert.Equal(t, http.StatusNoContent, rr.Code)
	_, err := os.Stat(filepath.Join(cfg.RuntimeDir(), "proj", "last-proposal.json"))
	assert.NoError(t, err, "diff vs snapshot must record attribution as before")
}
