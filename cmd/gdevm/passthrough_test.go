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

	code := runPassthrough([]string{"10m", "--reason", "need to curl a mirror"}, srv.URL, "/home/devm/proj")
	assert.Equal(t, 0, code)
	assert.Equal(t, "need to curl a mirror", got.Reason)
	assert.Equal(t, 600, got.DurationSeconds)
	assert.Equal(t, "/home/devm/proj", got.Cwd)
	assert.Equal(t, "guest", got.Source)
}

func TestPassthrough_ReasonRequired(t *testing.T) {
	stderr := captureStderr(t, func() {
		code := runPassthrough([]string{"5m"}, "http://ignored", "/x")
		assert.Equal(t, 2, code)
	})
	assert.Contains(t, stderr, "--reason is required")
}

// TestPassthrough_DurationRequired pins that omitting the positional
// duration fails fast with an actionable message — not a silent
// default. Silent defaults produced the "I ran approve but nothing
// opened" report: a 30s window that had expired by the time the
// operator got back to test.
func TestPassthrough_DurationRequired(t *testing.T) {
	stderr := captureStderr(t, func() {
		code := runPassthrough([]string{"--reason", "x"}, "http://ignored", "/x")
		assert.Equal(t, 2, code)
	})
	assert.Contains(t, stderr, "duration is required")
}

func TestPassthrough_RejectsSubsecondDuration(t *testing.T) {
	stderr := captureStderr(t, func() {
		code := runPassthrough([]string{"500ms", "--reason", "x"}, "http://ignored", "/x")
		assert.Equal(t, 2, code)
	})
	assert.Contains(t, stderr, "at least 1s")
}

func TestPassthrough_UnknownFlag(t *testing.T) {
	stderr := captureStderr(t, func() {
		code := runPassthrough([]string{"5m", "--reason", "x", "--bogus"}, "http://ignored", "/x")
		assert.Equal(t, 2, code)
	})
	assert.Contains(t, stderr, "unknown flag")
}

// TestPassthrough_ExtraPositionalRejected pins that a second
// non-flag arg is refused rather than silently discarded — the parser
// is strict enough that a typo like `gdevm passthrough 5m --reason x extra`
// surfaces the mistake.
func TestPassthrough_ExtraPositionalRejected(t *testing.T) {
	stderr := captureStderr(t, func() {
		code := runPassthrough([]string{"5m", "10m", "--reason", "x"}, "http://ignored", "/x")
		assert.Equal(t, 2, code)
	})
	assert.Contains(t, stderr, "unexpected extra arg")
}

// TestPassthrough_HelpPrintsUsage pins the -h / --help affordance.
// The user missed --for was the flag name because -h printed nothing;
// now it prints the usage block covering the positional duration.
func TestPassthrough_HelpPrintsUsage(t *testing.T) {
	stdout := captureStdout(t, func() {
		code := runPassthrough([]string{"--help"}, "http://ignored", "/x")
		assert.Equal(t, 0, code)
	})
	assert.Contains(t, stdout, "usage: gdevm passthrough <duration>")
	assert.Contains(t, stdout, "--reason")
}

func TestPassthrough_ForbiddenReturnsExit3(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "guest.passthrough is disabled", http.StatusForbidden)
	}))
	defer srv.Close()

	stderr := captureStderr(t, func() {
		code := runPassthrough([]string{"5m", "--reason", "x"}, srv.URL, "/x")
		assert.Equal(t, 3, code)
	})
	assert.Contains(t, stderr, "guest.passthrough is disabled")
}

func TestPassthrough_TransportErrorExit1(t *testing.T) {
	code := runPassthrough([]string{"5m", "--reason", "x"}, "http://127.0.0.1:1/passthrough", "/x")
	assert.Equal(t, 1, code)
}
