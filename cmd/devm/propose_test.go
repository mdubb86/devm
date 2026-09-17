package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMacPropose_SendsMetadataWithSource(t *testing.T) {
	var captured struct {
		Cwd    string `json:"cwd"`
		Reason string `json:"reason"`
		Kind   string `json:"kind"`
		Source string `json:"source"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/vm/resolve-project" {
			_ = json.NewEncoder(w).Encode(map[string]string{"name": "p", "state_dir": "/x/p"})
			return
		}
		if r.URL.Path == "/vm/propose" {
			require.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
			w.WriteHeader(http.StatusNoContent)
			return
		}
		t.Fatalf("unexpected path: %s", r.URL.Path)
	}))
	defer srv.Close()

	code := runMacPropose(srv.URL, "/Users/x/proj", "add example.com", "devm.yaml")
	assert.Equal(t, 0, code)
	assert.Equal(t, "/Users/x/proj", captured.Cwd)
	assert.Equal(t, "add example.com", captured.Reason)
	assert.Equal(t, "devm.yaml", captured.Kind)
	assert.Equal(t, "mac", captured.Source)
}

func TestMacPropose_EmptyReasonOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/vm/resolve-project" {
			_ = json.NewEncoder(w).Encode(map[string]string{"name": "p", "state_dir": "/x/p"})
			return
		}
		if r.URL.Path == "/vm/propose" {
			var body struct{ Reason string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			assert.Equal(t, "", body.Reason, "empty reason should be sent as-is")
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}))
	defer srv.Close()

	code := runMacPropose(srv.URL, "/Users/x/proj", "", "devm.yaml")
	assert.Equal(t, 0, code)
}

func TestMacPropose_ResolveFails_Returns2(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/vm/resolve-project" {
			http.Error(w, "resolve-project: cwd not registered", http.StatusNotFound)
			return
		}
	}))
	defer srv.Close()

	code := runMacPropose(srv.URL, "/Users/x/unknown", "test", "devm.yaml")
	assert.Equal(t, 2, code)
}

// TestMacPropose_TransportError_Returns1 pins M2's exit-code split: a
// resolve-project call that never reaches the daemon (socket down,
// nothing listening) must exit 1, not 2 — 2 is reserved for a daemon
// HTTP error response, which never happened here.
func TestMacPropose_TransportError_Returns1(t *testing.T) {
	code := runMacPropose("http://127.0.0.1:1", "/Users/x/proj", "test", "devm.yaml")
	assert.Equal(t, 1, code)
}

func TestMacPropose_DaemonBadRequest_Returns2(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/vm/resolve-project" {
			_ = json.NewEncoder(w).Encode(map[string]string{"name": "p", "state_dir": "/x/p"})
			return
		}
		if r.URL.Path == "/vm/propose" {
			http.Error(w, "propose: invalid kind", http.StatusBadRequest)
			return
		}
	}))
	defer srv.Close()

	code := runMacPropose(srv.URL, "/Users/x/proj", "test", "invalid.yaml")
	assert.Equal(t, 2, code)
}

func TestMacPropose_DaemonError_Returns1(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/vm/resolve-project" {
			_ = json.NewEncoder(w).Encode(map[string]string{"name": "p", "state_dir": "/x/p"})
			return
		}
		if r.URL.Path == "/vm/propose" {
			http.Error(w, "propose: internal error", http.StatusInternalServerError)
			return
		}
	}))
	defer srv.Close()

	code := runMacPropose(srv.URL, "/Users/x/proj", "test", "devm.yaml")
	assert.Equal(t, 1, code)
}
