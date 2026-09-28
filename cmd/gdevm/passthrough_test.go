package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPassthrough_HappyPath(t *testing.T) {
	var got passthroughRequestBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("queued\n"))
	}))
	defer srv.Close()

	code := runPassthrough([]string{"--reason", "need to curl a mirror", "--for", "10m"}, srv.URL, "/home/devm/proj")
	assert.Equal(t, 0, code)
	assert.Equal(t, "need to curl a mirror", got.Reason)
	assert.Equal(t, 600, got.DurationSeconds)
	assert.Equal(t, "/home/devm/proj", got.Cwd)
	assert.Equal(t, "guest", got.Source)
}

func TestPassthrough_ReasonRequired(t *testing.T) {
	stderr := captureStderr(t, func() {
		code := runPassthrough([]string{}, "http://ignored", "/x")
		assert.Equal(t, 2, code)
	})
	assert.Contains(t, stderr, "--reason is required")
}

func TestPassthrough_ForRequiresValue(t *testing.T) {
	stderr := captureStderr(t, func() {
		code := runPassthrough([]string{"--reason", "x", "--for"}, "http://ignored", "/x")
		assert.Equal(t, 2, code)
	})
	assert.Contains(t, stderr, "--for requires a value")
}

func TestPassthrough_ForRejectsSubsecond(t *testing.T) {
	stderr := captureStderr(t, func() {
		code := runPassthrough([]string{"--reason", "x", "--for", "500ms"}, "http://ignored", "/x")
		assert.Equal(t, 2, code)
	})
	assert.Contains(t, stderr, "at least 1s")
}

func TestPassthrough_UnknownArg(t *testing.T) {
	stderr := captureStderr(t, func() {
		code := runPassthrough([]string{"--reason", "x", "--bogus"}, "http://ignored", "/x")
		assert.Equal(t, 2, code)
	})
	assert.Contains(t, stderr, "unknown arg")
}

func TestPassthrough_ForbiddenReturnsExit3(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "guest.passthrough is disabled", http.StatusForbidden)
	}))
	defer srv.Close()

	stderr := captureStderr(t, func() {
		code := runPassthrough([]string{"--reason", "x"}, srv.URL, "/x")
		assert.Equal(t, 3, code)
	})
	assert.Contains(t, stderr, "guest.passthrough is disabled")
}

func TestPassthrough_TransportErrorExit1(t *testing.T) {
	code := runPassthrough([]string{"--reason", "x"}, "http://127.0.0.1:1/passthrough", "/x")
	assert.Equal(t, 1, code)
}
