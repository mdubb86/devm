package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecipes_List_HappyPath(t *testing.T) {
	body := `[{"name":"tool/foo","category":"tool","display_name":"Foo","description":"A foo"}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/list", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	stdout := captureStdout(t, func() {
		code := runRecipes(srv.URL, []string{"list"})
		assert.Equal(t, 0, code)
	})
	assert.Contains(t, stdout, `"tool/foo"`)
}

func TestRecipes_Get_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/get", r.URL.Path)
		require.Equal(t, "tool/foo", r.URL.Query().Get("name"))
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = io.WriteString(w, "# Foo\n\nBody text.\n")
	}))
	defer srv.Close()

	stdout := captureStdout(t, func() {
		code := runRecipes(srv.URL, []string{"get", "tool/foo"})
		assert.Equal(t, 0, code)
	})
	assert.Contains(t, stdout, "# Foo")
	assert.Contains(t, stdout, "Body text.")
}

func TestRecipes_Get_MissingRecipeExit3(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "recipe not found: tool/nope", http.StatusNotFound)
	}))
	defer srv.Close()

	stderr := captureStderr(t, func() {
		code := runRecipes(srv.URL, []string{"get", "tool/nope"})
		assert.Equal(t, 3, code)
	})
	assert.Contains(t, stderr, "not found")
}

func TestRecipes_AssetLs_HappyPath(t *testing.T) {
	body := `[{"path":"skills/one.md","size":42,"mode":420}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/asset/ls", r.URL.Path)
		require.Equal(t, "tool/foo", r.URL.Query().Get("name"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	stdout := captureStdout(t, func() {
		code := runRecipes(srv.URL, []string{"asset", "ls", "tool/foo"})
		assert.Equal(t, 0, code)
	})
	assert.Contains(t, stdout, "skills/one.md")
}

func TestRecipes_AssetGet_HappyPathRawBytes(t *testing.T) {
	payload := []byte{0x00, 0x01, 0x02, 0x03, 0xff, 'h', 'i'}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/asset/get", r.URL.Path)
		require.Equal(t, "tool/foo", r.URL.Query().Get("name"))
		require.Equal(t, "skills/one.md", r.URL.Query().Get("path"))
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	stdout := captureStdout(t, func() {
		code := runRecipes(srv.URL, []string{"asset", "get", "tool/foo", "skills/one.md"})
		assert.Equal(t, 0, code)
	})
	assert.Equal(t, string(payload), stdout)
}

func TestRecipes_AssetGet_InvalidPathExit2(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "asset path escapes recipe root", http.StatusBadRequest)
	}))
	defer srv.Close()

	stderr := captureStderr(t, func() {
		code := runRecipes(srv.URL, []string{"asset", "get", "tool/foo", "../evil"})
		assert.Equal(t, 2, code)
	})
	assert.Contains(t, stderr, "asset path escapes recipe root")
}

func TestRecipes_TransportErrorExit1(t *testing.T) {
	stderr := captureStderr(t, func() {
		code := runRecipes("http://127.0.0.1:1", []string{"list"})
		assert.Equal(t, 1, code)
	})
	assert.Contains(t, stderr, "cannot reach devm daemon")
}
