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

	code := doPost(srv.URL, "/home/devm/proj", "feature-x", "adding foo", "devm.yaml")
	assert.Equal(t, 0, code)

	assert.Equal(t, "/home/devm/proj", got.Cwd)
	assert.Equal(t, "feature-x", got.Branch)
	assert.Equal(t, "adding foo", got.Reason)
	assert.Equal(t, "devm.yaml", got.Kind)
	assert.Equal(t, "guest", got.Source)
}

func TestPost_TransportErrorExit1(t *testing.T) {
	code := doPost("http://127.0.0.1:1/propose", "/x", "", "", "devm.yaml")
	assert.Equal(t, 1, code)
}

func TestPost_Daemon400Exit2(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad yaml", http.StatusBadRequest)
	}))
	defer srv.Close()
	code := doPost(srv.URL, "/x", "", "", "devm.yaml")
	assert.Equal(t, 2, code)
}

func TestPost_Daemon404Exit2(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	stderr := captureStderr(t, func() {
		code := doPost(srv.URL, "/x", "", "", "devm.yaml")
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

	code := run([]string{"--reason", "test reason"}, srv.URL, "/somewhere")
	assert.Equal(t, 0, code)
	assert.Equal(t, "test reason", got.Reason)
	assert.Equal(t, "devm.yaml", got.Kind)
}

func TestRun_KindFlag(t *testing.T) {
	var got proposeBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	code := run([]string{"--kind", "devm.me.yaml"}, srv.URL, "/somewhere")
	assert.Equal(t, 0, code)
	assert.Equal(t, "devm.me.yaml", got.Kind)
}

func TestRun_NoArgs(t *testing.T) {
	var got proposeBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	code := run([]string{}, srv.URL, "/somewhere")
	assert.Equal(t, 0, code)
	assert.Equal(t, "", got.Reason)
	assert.Equal(t, "devm.yaml", got.Kind)
	assert.Equal(t, "guest", got.Source)
}

func TestRun_ReasonAndKindFlags(t *testing.T) {
	var got proposeBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	code := run([]string{"--reason", "r", "--kind", "devm.me.yaml"}, srv.URL, "/somewhere")
	assert.Equal(t, 0, code)
	assert.Equal(t, "r", got.Reason)
	assert.Equal(t, "devm.me.yaml", got.Kind)
}

func TestRun_ReasonMissingValueExit2(t *testing.T) {
	stderr := captureStderr(t, func() {
		code := run([]string{"--reason"}, "http://127.0.0.1:1", "/somewhere")
		assert.Equal(t, 2, code)
	})
	assert.Contains(t, stderr, "propose: --reason requires a value")
}

func TestRun_KindMissingValueExit2(t *testing.T) {
	stderr := captureStderr(t, func() {
		code := run([]string{"--kind"}, "http://127.0.0.1:1", "/somewhere")
		assert.Equal(t, 2, code)
	})
	assert.Contains(t, stderr, "propose: --kind requires a value")
}

func TestRun_UnknownArgExit2(t *testing.T) {
	stderr := captureStderr(t, func() {
		code := run([]string{"--unknown"}, "http://127.0.0.1:1", "/somewhere")
		assert.Equal(t, 2, code)
	})
	assert.Contains(t, stderr, "propose: unknown arg")
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w

	fn()

	require.NoError(t, w.Close())
	os.Stderr = orig

	out, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(out)
}

func TestGitBranch_NotAGitRepo_ReturnsEmpty(t *testing.T) {
	tmp := t.TempDir()
	got := gitBranch(tmp)
	assert.Equal(t, "", got, "must not error on non-git dir")
}
