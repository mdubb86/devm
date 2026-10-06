package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

	code := doPopPost(srv.URL, "/home/devm/foo.html", false, false, false, nil)
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

	code := doPopPost(srv.URL, "/home/devm/foo.html", true, true, true, []string{"-a", "Preview"})
	assert.Equal(t, 0, code)
	assert.True(t, got.Native)
	assert.True(t, got.IsDir)
	assert.True(t, got.IsHTML)
	assert.Equal(t, []string{"-a", "Preview"}, got.OpenArgs)
}

func TestDoPopPost_SingleFlagNotSwapped(t *testing.T) {
	cases := []struct {
		name          string
		isDir, isHTML bool
	}{
		{"dir only", true, false},
		{"html only", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got popBody
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			code := doPopPost(srv.URL, "/home/devm/x", false, c.isDir, c.isHTML, nil)
			assert.Equal(t, 0, code)
			assert.Equal(t, c.isDir, got.IsDir)
			assert.Equal(t, c.isHTML, got.IsHTML)
		})
	}
}

func TestDoPopPost_TransportErrorExit1(t *testing.T) {
	code := doPopPost("http://127.0.0.1:1/pop", "/x/y", false, false, false, nil)
	assert.Equal(t, 1, code)
}

func TestDoPopPost_404Exit2(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such file", http.StatusNotFound)
	}))
	defer srv.Close()
	assert.Equal(t, 2, doPopPost(srv.URL, "/x/y", false, false, false, nil))
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

func TestClassifyPopTarget_HTMLByExtension(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.html", "b.HTML", "c.htm", "d.Htm"} {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, []byte("<p>x</p>"), 0o644))
		isDir, isHTML, err := classifyPopTarget(path)
		require.NoError(t, err, name)
		assert.False(t, isDir, name)
		assert.True(t, isHTML, name)
	}
}

func TestClassifyPopTarget_DirIsDirNotHTML(t *testing.T) {
	isDir, isHTML, err := classifyPopTarget(t.TempDir())
	require.NoError(t, err)
	assert.True(t, isDir)
	assert.False(t, isHTML)
}

func TestClassifyPopTarget_PlainFileNeitherDirNorHTML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.txt")
	require.NoError(t, os.WriteFile(path, []byte("y"), 0o644))
	isDir, isHTML, err := classifyPopTarget(path)
	require.NoError(t, err)
	assert.False(t, isDir)
	assert.False(t, isHTML)
}

func TestClassifyPopTarget_MissingErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.html")
	_, _, err := classifyPopTarget(missing)
	require.Error(t, err)
	assert.ErrorContains(t, err, "no such file")
}

func TestClassifyPopTarget_StatErrorWrapped(t *testing.T) {
	reg := filepath.Join(t.TempDir(), "regular.txt")
	require.NoError(t, os.WriteFile(reg, []byte("x"), 0o644))
	// A regular file used as a parent directory makes stat fail with
	// ENOTDIR on Linux and macOS: a non-ENOENT error, no chmod or
	// non-root uid needed.
	_, _, err := classifyPopTarget(filepath.Join(reg, "child.html"))
	require.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), "stat "), err.Error())
	assert.NotContains(t, err.Error(), "no such file")
}
