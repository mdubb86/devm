package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDoPopPost_SendsBodyAndStreamsResponse(t *testing.T) {
	var got popBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("https://files.p.test/files/local/foo\n"))
	}))
	defer srv.Close()

	code := doPopPost(srv.URL, "/home/devm/foo.html", false, nil)
	assert.Equal(t, 0, code)
	assert.Equal(t, "/home/devm/foo.html", got.GuestPath)
	assert.False(t, got.Native)
	assert.Nil(t, got.OpenArgs)
}

func TestDoPopPost_PassesNativeAndOpenArgs(t *testing.T) {
	var got popBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	code := doPopPost(srv.URL, "/home/devm/foo.html", true, []string{"-a", "Preview"})
	assert.Equal(t, 0, code)
	assert.True(t, got.Native)
	assert.Equal(t, []string{"-a", "Preview"}, got.OpenArgs)
}

func TestDoPopPost_TransportErrorExit1(t *testing.T) {
	code := doPopPost("http://127.0.0.1:1/pop", "/x/y", false, nil)
	assert.Equal(t, 1, code)
}

func TestDoPopPost_404Exit2(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such file", http.StatusNotFound)
	}))
	defer srv.Close()
	assert.Equal(t, 2, doPopPost(srv.URL, "/x/y", false, nil))
}

func TestAbsolutizeGuestPath_Absolute(t *testing.T) {
	got, err := absolutizeGuestPath("/home/devm/foo")
	require.NoError(t, err)
	assert.Equal(t, "/home/devm/foo", got)
}

func TestAbsolutizeGuestPath_Relative(t *testing.T) {
	tmp := t.TempDir()
	// macOS tempdirs sit behind the /var → /private/var symlink;
	// os.Getwd after Chdir returns the resolved path, so compare against
	// the resolved form.
	resolved, err := filepath.EvalSymlinks(tmp)
	require.NoError(t, err)
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	require.NoError(t, os.Chdir(tmp))

	got, err := absolutizeGuestPath("foo/bar.html")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(resolved, "foo/bar.html"), got)
}

func TestPopMain_RequiresPath(t *testing.T) {
	// capture stderr so test output isn't noisy.
	origErr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	t.Cleanup(func() {
		os.Stderr = origErr
		w.Close()
		r.Close()
	})
	code := popMain(nil)
	assert.Equal(t, 2, code)
}
