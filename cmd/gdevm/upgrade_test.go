package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpgrade_HappyPathPrintsSummary(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/refresh-bundle", r.URL.Path)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "bundle refreshed for proj\n  old fingerprint: old\n  new fingerprint: new\n")
	}))
	defer srv.Close()

	stdout := captureStdout(t, func() {
		code := runUpgrade(srv.URL + "/refresh-bundle")
		assert.Equal(t, 0, code)
	})
	assert.Contains(t, stdout, "bundle refreshed")
	assert.Contains(t, stdout, "new fingerprint: new")
}

func TestUpgrade_Precondition412SurfacesActionableError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "refresh-bundle: no state snapshot", http.StatusPreconditionFailed)
	}))
	defer srv.Close()

	stderr := captureStderr(t, func() {
		code := runUpgrade(srv.URL + "/refresh-bundle")
		assert.Equal(t, 2, code)
	})
	assert.Contains(t, stderr, "no state snapshot")
}

func TestUpgrade_TransportErrorExit1(t *testing.T) {
	stderr := captureStderr(t, func() {
		code := runUpgrade("http://127.0.0.1:1/refresh-bundle")
		assert.Equal(t, 1, code)
	})
	assert.Contains(t, stderr, "cannot reach devm daemon")
}

// captureStdout mirrors captureStderr in propose_test.go: redirect
// os.Stdout for the duration of fn and return what it wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	fn()
	w.Close()
	return <-done
}
