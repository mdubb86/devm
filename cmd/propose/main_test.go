package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPost_SendsBase64EncodedYAML(t *testing.T) {
	var got proposeBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	yamlBytes := []byte("name: myproj\n")
	code := doPost(srv.URL, yamlBytes, "/home/devm/proj", "feature-x", "adding foo")
	assert.Equal(t, 0, code)

	assert.Equal(t, "devm.yaml", got.Kind)
	assert.Equal(t, "/home/devm/proj", got.Cwd)
	assert.Equal(t, "feature-x", got.Branch)
	assert.Equal(t, "adding foo", got.Reason)

	decoded, err := base64.StdEncoding.DecodeString(got.YAML)
	require.NoError(t, err)
	assert.Equal(t, yamlBytes, decoded)
}

func TestPost_TransportErrorExit1(t *testing.T) {
	// Dial a port nothing is listening on.
	code := doPost("http://127.0.0.1:1/propose", []byte("x"), "/x", "", "")
	assert.Equal(t, 1, code)
}

func TestPost_Daemon400Exit2(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad yaml", http.StatusBadRequest)
	}))
	defer srv.Close()
	code := doPost(srv.URL, []byte("x"), "/x", "", "")
	assert.Equal(t, 2, code)
}

func TestPost_Daemon404Exit2(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	stderr := captureStderr(t, func() {
		code := doPost(srv.URL, []byte("x"), "/x", "", "")
		assert.Equal(t, 2, code)
	})
	assert.Contains(t, stderr, "daemon does not support propose channel — upgrade the Mac side")
}

// captureStderr redirects os.Stderr for the duration of fn and returns
// everything written to it.
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
	// Point cwd at a temp dir that isn't a git repo.
	tmp := t.TempDir()
	got := gitBranch(tmp)
	assert.Equal(t, "", got, "must not error on non-git dir")
}

// End-to-end: reads YAML from stdin, sends it.
func TestMain_ReadsStdinAndPosts(t *testing.T) {
	var got proposeBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	yamlBody := []byte("name: from-stdin\n")
	code := run([]string{"--reason", "r"}, strings.NewReader(string(yamlBody)), srv.URL, "/somewhere")
	assert.Equal(t, 0, code)

	decoded, _ := base64.StdEncoding.DecodeString(got.YAML)
	assert.Equal(t, yamlBody, decoded)
	assert.Equal(t, "r", got.Reason)
}

// End-to-end: reads YAML from path arg.
func TestMain_ReadsPathAndPosts(t *testing.T) {
	var got proposeBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	tmp := t.TempDir() + "/devm.yaml"
	require.NoError(t, os.WriteFile(tmp, []byte("name: from-path\n"), 0644))

	code := run([]string{tmp}, nil, srv.URL, "/somewhere")
	assert.Equal(t, 0, code)
	decoded, _ := base64.StdEncoding.DecodeString(got.YAML)
	assert.Equal(t, []byte("name: from-path\n"), decoded)
}

// Test: --reason flag works with file path arg.
func TestMain_ReasonFlagWithPathArg(t *testing.T) {
	var got proposeBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	tmp := t.TempDir() + "/devm.yaml"
	require.NoError(t, os.WriteFile(tmp, []byte("name: test\n"), 0644))

	code := run([]string{"--reason", "testing purpose", tmp}, nil, srv.URL, "/somewhere")
	assert.Equal(t, 0, code)
	assert.Equal(t, "testing purpose", got.Reason)
}

// Test: file read error returns exit code 1.
func TestMain_FileReadErrorExit1(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	code := run([]string{"/nonexistent/path/devm.yaml"}, nil, srv.URL, "/somewhere")
	assert.Equal(t, 1, code)
}
