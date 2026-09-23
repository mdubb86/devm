package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
		Kind   string `json:"kind"`
		Source string `json:"source"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/vm/propose", r.URL.Path)
		require.Equal(t, "p", r.URL.Query().Get("project"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	code := runMacPropose(srv.URL, "add example.com", "devm.yaml")
	assert.Equal(t, 0, code)
	assert.Equal(t, "/Users/x/proj", captured.Cwd)
	assert.Equal(t, "add example.com", captured.Reason)
	assert.Equal(t, "devm.yaml", captured.Kind)
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

	code := runMacPropose(srv.URL, "", "devm.yaml")
	assert.Equal(t, 0, code)
}

func TestPropose_DiscoverFails_Returns2(t *testing.T) {
	withDiscoverProject(t, LocalProject{}, errors.New("discover-project: no devm.yaml found"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request to daemon: %s", r.URL.Path)
	}))
	defer srv.Close()

	code := runMacPropose(srv.URL, "test", "devm.yaml")
	assert.Equal(t, 2, code)
}

// TestPropose_TransportError_Returns1 pins the exit-code split: a
// propose call that never reaches the daemon (socket down, nothing
// listening) must exit 1, not 2 — 2 is reserved for a daemon HTTP error
// response, which never happened here.
func TestPropose_TransportError_Returns1(t *testing.T) {
	withDiscoverProject(t, LocalProject{MacCwd: "/Users/x/proj", Name: "p"}, nil)

	code := runMacPropose("http://127.0.0.1:1", "test", "devm.yaml")
	assert.Equal(t, 1, code)
}

func TestPropose_DaemonBadRequest_Returns2(t *testing.T) {
	withDiscoverProject(t, LocalProject{MacCwd: "/Users/x/proj", Name: "p"}, nil)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/vm/propose", r.URL.Path)
		http.Error(w, "propose: invalid kind", http.StatusBadRequest)
	}))
	defer srv.Close()

	code := runMacPropose(srv.URL, "test", "invalid.yaml")
	assert.Equal(t, 2, code)
}

func TestPropose_DaemonError_Returns1(t *testing.T) {
	withDiscoverProject(t, LocalProject{MacCwd: "/Users/x/proj", Name: "p"}, nil)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/vm/propose", r.URL.Path)
		http.Error(w, "propose: internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	code := runMacPropose(srv.URL, "test", "devm.yaml")
	assert.Equal(t, 1, code)
}
