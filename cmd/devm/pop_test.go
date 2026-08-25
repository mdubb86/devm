package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveMacMode_CwdRelativeExists — pop mac finds a file at
// cwd-relative and returns its path.
func TestResolveMacMode_CwdRelativeExists(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "devm.yaml"), []byte("project:\n  name: t\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "local.txt"), []byte("x"), 0o644))

	got, err := resolveMacMode("local.txt", dir, nil)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "local.txt"), got)
}

// TestResolveMacMode_FallsBackToProjectRoot — file at project root
// but not cwd.
func TestResolveMacMode_FallsBackToProjectRoot(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "devm.yaml"), []byte("project:\n  name: t\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "top.txt"), []byte("x"), 0o644))
	sub := filepath.Join(root, "sub")
	require.NoError(t, os.MkdirAll(sub, 0o755))

	got, err := resolveMacMode("top.txt", sub, nil)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "top.txt"), got)
}

// TestResolveMacMode_RefusesResolutionIntoVolume — a candidate that
// EvalSymlinks resolves into a known storage path must be refused,
// even if the surface path looks Mac-native.
func TestResolveMacMode_RefusesResolutionIntoVolume(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "devm.yaml"), []byte("project:\n  name: t\n"), 0o644))
	storage := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(storage, "vm-file.png"), []byte("x"), 0o644))
	// The .vm symlink into storage.
	require.NoError(t, os.Symlink(storage, filepath.Join(root, ".vm")))

	registry := []serviceapi.WorkspaceEntry{
		{ProjectName: "t", GuestPath: root, StoragePath: storage},
	}

	_, err := resolveMacMode(".vm/vm-file.png", root, registry)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "devm-managed volume")
	assert.Contains(t, err.Error(), "pop vm")
}

// TestResolveMacMode_EscapeArgFromInsideVMSubdir — user in a .vm/
// subdir with an arg that navigates OUT of .vm/ to a Mac-native file
// must be allowed.
func TestResolveMacMode_EscapeArgFromInsideVMSubdir(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "devm.yaml"), []byte("project:\n  name: t\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "mac-native.txt"), []byte("x"), 0o644))
	storage := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(storage, "src"), 0o755))
	require.NoError(t, os.Symlink(storage, filepath.Join(root, ".vm")))

	registry := []serviceapi.WorkspaceEntry{
		{ProjectName: "t", GuestPath: root, StoragePath: storage},
	}

	// User is in .vm/src, uses ../../mac-native.txt to escape.
	cwd := filepath.Join(root, ".vm", "src")
	got, err := resolveMacMode("../../mac-native.txt", cwd, registry)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "mac-native.txt"), got)
}

// TestResolveVMMode_ProjectRootRelative — vm mode always resolves
// against the project's guest root, ignoring cwd.
func TestResolveVMMode_ProjectRootRelative(t *testing.T) {
	root := t.TempDir()    // Mac project mirror dir
	require.NoError(t, os.WriteFile(filepath.Join(root, "devm.yaml"), []byte("project:\n  name: t\n"), 0o644))
	storage := t.TempDir() // Volume storage
	require.NoError(t, os.WriteFile(filepath.Join(storage, "vm-file.png"), []byte("x"), 0o644))

	registry := []serviceapi.WorkspaceEntry{
		{ProjectName: "t", GuestPath: root, StoragePath: storage},
	}

	got, err := resolveVMMode("vm-file.png", root, registry)
	require.NoError(t, err)
	// Pretty .vm/-form path is what open should get.
	assert.Equal(t, filepath.Join(root, ".vm", "vm-file.png"), got)
}

func TestResolveVMMode_NotFound(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "devm.yaml"), []byte("project:\n  name: t\n"), 0o644))
	storage := t.TempDir()
	registry := []serviceapi.WorkspaceEntry{
		{ProjectName: "t", GuestPath: root, StoragePath: storage},
	}
	_, err := resolveVMMode("nope.png", root, registry)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no such file")
}
