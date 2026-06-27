package ironproxy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPathFromDir_DevLayout(t *testing.T) {
	tmp := t.TempDir()
	binary := filepath.Join(tmp, "iron-proxy")
	require.NoError(t, os.WriteFile(binary, []byte("#!/bin/sh"), 0755))

	got, err := pathFromDir(tmp)
	require.NoError(t, err)
	assert.Equal(t, binary, got)
}

func TestPathFromDir_InstalledLayout(t *testing.T) {
	tmp := t.TempDir()
	share := filepath.Join(tmp, "..", "share", "devm", "bin")
	require.NoError(t, os.MkdirAll(share, 0755))
	binary := filepath.Join(share, "iron-proxy")
	require.NoError(t, os.WriteFile(binary, []byte("#!/bin/sh"), 0755))

	got, err := pathFromDir(tmp)
	require.NoError(t, err)
	want, _ := filepath.EvalSymlinks(binary)
	resolved, _ := filepath.EvalSymlinks(got)
	assert.Equal(t, want, resolved)
}

func TestPathFromDir_NotFound(t *testing.T) {
	tmp := t.TempDir()
	_, err := pathFromDir(tmp)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "iron-proxy not found")
}
