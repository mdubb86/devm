package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveProjectFromCwd_Match(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/vm/resolve-project", r.URL.Path)
		require.NotEmpty(t, r.URL.Query().Get("cwd"))
		_ = json.NewEncoder(w).Encode(map[string]string{
			"name":      "shelfmates",
			"state_dir": "/some/state/shelfmates",
		})
	}))
	defer srv.Close()

	got, err := resolveProjectFromURL(srv.URL, "/Users/x/code/shelfmates")
	require.NoError(t, err)
	assert.Equal(t, "shelfmates", got.Name)
	assert.Equal(t, "/some/state/shelfmates", got.StateDir)
}

func TestResolveProjectFromCwd_404BubblesHint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "resolve-project: /x is not a devm project; run 'devm init <name>' to register it", http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := resolveProjectFromURL(srv.URL, "/x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "devm init")
}

func TestResolveProjectFromCwd_TransportErrorSurfaces(t *testing.T) {
	_, err := resolveProjectFromURL("http://127.0.0.1:1", "/x")
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "connection") || strings.Contains(err.Error(), "refused"))
}
