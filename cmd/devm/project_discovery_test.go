package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDiscoverProject_HappyPath(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "devm.yaml"),
		[]byte("project:\n  name: shelfmates\n"), 0o644))
	rp, err := discoverProjectFromCwd(dir)
	require.NoError(t, err)
	require.Equal(t, "shelfmates", rp.Name)
	require.Equal(t, dir, rp.MacCwd)
}

func TestDiscoverProject_WalksUpFromSubdir(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "devm.yaml"),
		[]byte("project:\n  name: sm\n"), 0o644))
	sub := filepath.Join(root, "a", "b", "c")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	rp, err := discoverProjectFromCwd(sub)
	require.NoError(t, err)
	require.Equal(t, "sm", rp.Name)
	require.Equal(t, root, rp.MacCwd)
}

func TestDiscoverProject_NotInProject(t *testing.T) {
	dir := t.TempDir() // empty; no devm.yaml anywhere up-tree
	_, err := discoverProjectFromCwd(dir)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not inside a devm project")
}
