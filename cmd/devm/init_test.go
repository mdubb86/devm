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

func TestInit_HappyPath(t *testing.T) {
	var captured struct {
		Name string
		Cwd  string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.Name = r.URL.Query().Get("name")
		captured.Cwd = r.URL.Query().Get("cwd")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"name":        captured.Name,
			"config_path": "/some/state/" + captured.Name + "/devm.yaml",
		})
	}))
	defer srv.Close()

	out, err := runInit(srv.URL, "/Users/x/shelfmates", "shelfmates")
	require.NoError(t, err)
	assert.Contains(t, out, "shelfmates")
	assert.Equal(t, "shelfmates", captured.Name)
	assert.Equal(t, "/Users/x/shelfmates", captured.Cwd)
}

func TestInit_DaemonConflictSurfacesMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "register-project: project \"foo\" already exists at /some/dir", http.StatusConflict)
	}))
	defer srv.Close()

	_, err := runInit(srv.URL, "/x", "foo")
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "already exists"))
}
