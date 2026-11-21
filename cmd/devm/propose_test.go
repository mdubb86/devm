package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProposeCmd_FingerprintDrift_SurfacesActionableErrorBeforeDaemon
// pins that `devm propose` runs the daemon fingerprint handshake at
// RunE entry and short-circuits with the "run `devm install`" error
// when the daemon is stale. Without this gate, propose fell through
// to the daemon's /vm/propose handler and surfaced whatever field-
// level rejection the old daemon produced (e.g. "unknown field:
// swap" from a pre-swap daemon), which looked like a devm.yaml bug
// to the user after a `brew upgrade` had swapped the CLI but not
// the daemon.
func TestProposeCmd_FingerprintDrift_SurfacesActionableErrorBeforeDaemon(t *testing.T) {
	origFingerprint := Fingerprint
	Fingerprint = "fp-cli-new"
	t.Cleanup(func() { Fingerprint = origFingerprint })

	// Stand up a handshake daemon with a different fingerprint.
	// RegisterHandshakeHandler only mounts /handshake — /vm/propose
	// is NOT registered, so if the handshake gate were missing,
	// runMacProposeWithClient would reach the daemon, hit 404, and
	// return 1 ("transport error"). The gate catches it first and
	// returns the actionable error.
	cleanup := startHandshakeDaemon(t,
		serviceapi.Build{Fingerprint: "fp-daemon-old", BinaryPath: "/daemon/old"},
		nil)
	defer cleanup()

	// The RunE needs a project identity to propose for, but drift
	// check runs first and short-circuits before discoverProject
	// would matter. Stub it anyway so a regression-missing gate
	// wouldn't fail for the wrong reason.
	withDiscoverProject(t, LocalProject{MacCwd: "/tmp/proj", Name: "p"}, nil)

	proposeCmd.SetContext(context.Background())
	err := proposeCmd.RunE(proposeCmd, nil)
	require.Error(t, err, "propose must fail-fast on fingerprint drift")
	assert.Contains(t, err.Error(), "devm daemon is out of sync with this CLI")
	assert.Contains(t, err.Error(), "fp-daemon-old")
	assert.Contains(t, err.Error(), "fp-cli-new")
	assert.Contains(t, err.Error(), "devm install",
		"error must name the exact recovery command")

	// identity.Prod pulled in so the import is used even if the
	// test body ever stops referencing it directly.
	_ = identity.Prod
}

func withDiscoverProject(t *testing.T, rp LocalProject, err error) {
	t.Helper()
	orig := discoverProjectFn
	discoverProjectFn = func() (LocalProject, error) {
		return rp, err
	}
	t.Cleanup(func() { discoverProjectFn = orig })
}

func TestPropose_SendsMetadataWithSource(t *testing.T) {
	withDiscoverProject(t, LocalProject{MacCwd: "/Users/x/proj", Name: "p"}, nil)

	var captured struct {
		Cwd    string `json:"cwd"`
		Reason string `json:"reason"`
		Source string `json:"source"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/vm/propose", r.URL.Path)
		require.Equal(t, "p", r.URL.Query().Get("project"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	code := runMacPropose(srv.URL, "add example.com")
	assert.Equal(t, 0, code)
	assert.Equal(t, "/Users/x/proj", captured.Cwd)
	assert.Equal(t, "add example.com", captured.Reason)
	assert.Equal(t, "mac", captured.Source)
}

func TestPropose_EmptyReasonOK(t *testing.T) {
	withDiscoverProject(t, LocalProject{MacCwd: "/Users/x/proj", Name: "p"}, nil)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/vm/propose", r.URL.Path)
		var body struct{ Reason string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, "", body.Reason, "empty reason should be sent as-is")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	code := runMacPropose(srv.URL, "")
	assert.Equal(t, 0, code)
}

func TestPropose_DiscoverFails_Returns2(t *testing.T) {
	withDiscoverProject(t, LocalProject{}, errors.New("discover-project: no devm.yaml found"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request to daemon: %s", r.URL.Path)
	}))
	defer srv.Close()

	code := runMacPropose(srv.URL, "test")
	assert.Equal(t, 2, code)
}

// TestPropose_TransportError_Returns1 pins the exit-code split: a
// propose call that never reaches the daemon (socket down, nothing
// listening) must exit 1, not 2 — 2 is reserved for a daemon HTTP error
// response, which never happened here.
func TestPropose_TransportError_Returns1(t *testing.T) {
	withDiscoverProject(t, LocalProject{MacCwd: "/Users/x/proj", Name: "p"}, nil)

	code := runMacPropose("http://127.0.0.1:1", "test")
	assert.Equal(t, 1, code)
}

func TestPropose_DaemonBadRequest_Returns2(t *testing.T) {
	withDiscoverProject(t, LocalProject{MacCwd: "/Users/x/proj", Name: "p"}, nil)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/vm/propose", r.URL.Path)
		http.Error(w, "propose: invalid yaml", http.StatusBadRequest)
	}))
	defer srv.Close()

	code := runMacPropose(srv.URL, "test")
	assert.Equal(t, 2, code)
}

func TestPropose_DaemonError_Returns1(t *testing.T) {
	withDiscoverProject(t, LocalProject{MacCwd: "/Users/x/proj", Name: "p"}, nil)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/vm/propose", r.URL.Path)
		http.Error(w, "propose: internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	code := runMacPropose(srv.URL, "test")
	assert.Equal(t, 1, code)
}
