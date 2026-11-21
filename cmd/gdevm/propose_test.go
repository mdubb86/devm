package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPost_SendsMetadata(t *testing.T) {
	var got proposeBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	code := doPost(srv.URL, "/home/devm/proj", "feature-x", "adding foo")
	assert.Equal(t, 0, code)

	assert.Equal(t, "/home/devm/proj", got.Cwd)
	assert.Equal(t, "feature-x", got.Branch)
	assert.Equal(t, "adding foo", got.Reason)
	assert.Equal(t, "guest", got.Source)
}

func TestPost_TransportErrorExit1(t *testing.T) {
	code := doPost("http://127.0.0.1:1/propose", "/x", "", "")
	assert.Equal(t, 1, code)
}

func TestPost_Daemon400Exit2(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad yaml", http.StatusBadRequest)
	}))
	defer srv.Close()
	code := doPost(srv.URL, "/x", "", "")
	assert.Equal(t, 2, code)
}

func TestPost_Daemon404Exit2(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	stderr := captureStderr(t, func() {
		code := doPost(srv.URL, "/x", "", "")
		assert.Equal(t, 2, code)
	})
	assert.Contains(t, stderr, "daemon does not support propose channel — upgrade the Mac side")
}

func TestRun_ReasonFlag(t *testing.T) {
	var got proposeBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	code := runPropose([]string{"--reason", "test reason"}, srv.URL, "/somewhere")
	assert.Equal(t, 0, code)
	assert.Equal(t, "test reason", got.Reason)
}

func TestRun_NoArgs(t *testing.T) {
	var got proposeBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	code := runPropose([]string{}, srv.URL, "/somewhere")
	assert.Equal(t, 0, code)
	assert.Equal(t, "", got.Reason)
	assert.Equal(t, "guest", got.Source)
}

func TestRun_ReasonMissingValue(t *testing.T) {
	stderr := captureStderr(t, func() {
		code := runPropose([]string{"--reason"}, "http://ignored", "/x")
		assert.Equal(t, 2, code)
	})
	assert.Contains(t, stderr, "--reason requires a value")
}

func TestRun_UnknownArg(t *testing.T) {
	stderr := captureStderr(t, func() {
		code := runPropose([]string{"--bogus"}, "http://ignored", "/x")
		assert.Equal(t, 2, code)
	})
	assert.Contains(t, stderr, "unknown arg")
}

// TestRun_HelpPrintsUsage pins the -h / --help affordance. Discovering
// what to pass by reading strings out of the binary was a real user
// pain point.
func TestRun_HelpPrintsUsage(t *testing.T) {
	stdout := captureStdout(t, func() {
		code := runPropose([]string{"--help"}, "http://ignored", "/x")
		assert.Equal(t, 0, code)
	})
	assert.Contains(t, stdout, "usage: gdevm propose")
	assert.Contains(t, stdout, "--reason")
}

// captureStderr redirects os.Stderr for the duration of fn and returns
// whatever it wrote.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	fn()
	w.Close()
	return <-done
}
