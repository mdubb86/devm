package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolvePopTarget_ProjectRootRelative — a relative path resolves
// through the label→mirror table to the primary repo's mirror dir,
// with no `.vm`-suffixed indirection.
func TestResolvePopTarget_ProjectRootRelative(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate cfg.RuntimeDir() from the real HOME
	repoRoot := t.TempDir()
	url := "https://example.com/repo.git"
	primary := true
	pcfg := schema.Config{
		Project: schema.Project{Name: "t"},
		Repos: map[string]schema.RepoConfig{
			"main": {URL: &url, Primary: &primary},
		},
	}

	mirrorDir := filepath.Join(cfg.RuntimeDir(), "t", "repo")
	require.NoError(t, os.MkdirAll(mirrorDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(mirrorDir, "vm-file.png"), []byte("x"), 0o644))

	got, err := resolvePopTarget("vm-file.png", repoRoot, pcfg)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(mirrorDir, "vm-file.png"), got)
	assert.NotContains(t, got, ".vm/")
}

// TestResolvePopTarget_NestedRelativePath — a relative path with
// subdirectories resolves the same way.
func TestResolvePopTarget_NestedRelativePath(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate cfg.RuntimeDir() from the real HOME
	repoRoot := t.TempDir()
	url := "https://example.com/repo.git"
	primary := true
	pcfg := schema.Config{
		Project: schema.Project{Name: "t3"},
		Repos: map[string]schema.RepoConfig{
			"main": {URL: &url, Primary: &primary},
		},
	}

	mirrorDir := filepath.Join(cfg.RuntimeDir(), "t3", "repo")
	require.NoError(t, os.MkdirAll(filepath.Join(mirrorDir, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(mirrorDir, "sub", "near.png"), []byte("x"), 0o644))

	got, err := resolvePopTarget("sub/near.png", repoRoot, pcfg)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(mirrorDir, "sub", "near.png"), got)
}

// TestResolvePopTarget_AbsoluteGuestPath — an absolute guest-side path
// (e.g. pasted from guest output) resolves directly, regardless of
// repoRoot.
func TestResolvePopTarget_AbsoluteGuestPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate cfg.RuntimeDir() from the real HOME
	repoRoot := t.TempDir()
	url := "https://example.com/repo.git"
	primary := true
	pcfg := schema.Config{
		Project: schema.Project{Name: "t2"},
		Repos: map[string]schema.RepoConfig{
			"main": {URL: &url, Primary: &primary},
		},
	}

	mirrorDir := filepath.Join(cfg.RuntimeDir(), "t2", "repo")
	require.NoError(t, os.MkdirAll(filepath.Join(mirrorDir, "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(mirrorDir, "src", "abs.png"), []byte("x"), 0o644))

	got, err := resolvePopTarget("/home/devm/repo/src/abs.png", repoRoot, pcfg)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(mirrorDir, "src", "abs.png"), got)
}

// TestResolvePopTarget_NotFound — resolving to a mirror path whose
// file doesn't exist is an error, not a silent open of a missing file.
func TestResolvePopTarget_NotFound(t *testing.T) {
	repoRoot := t.TempDir()
	url := "https://example.com/repo.git"
	primary := true
	pcfg := schema.Config{
		Project: schema.Project{Name: "t4"},
		Repos: map[string]schema.RepoConfig{
			"main": {URL: &url, Primary: &primary},
		},
	}

	_, err := resolvePopTarget("nope.png", repoRoot, pcfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no such file")
}

// TestResolvePopTarget_NoMirroredEntries — a project with no repos at
// all has no primary tree to resolve a relative path against.
func TestResolvePopTarget_NoMirroredEntries(t *testing.T) {
	repoRoot := t.TempDir()
	pcfg := schema.Config{Project: schema.Project{Name: "t5"}}

	_, err := resolvePopTarget("anything.png", repoRoot, pcfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no primary repo")
}

// TestResolvePopTarget_AbsolutePathOutsideAnyEntry — an absolute path
// that doesn't fall under any mirrored repo/volume is an error.
func TestResolvePopTarget_AbsolutePathOutsideAnyEntry(t *testing.T) {
	repoRoot := t.TempDir()
	url := "https://example.com/repo.git"
	primary := true
	pcfg := schema.Config{
		Project: schema.Project{Name: "t6"},
		Repos: map[string]schema.RepoConfig{
			"main": {URL: &url, Primary: &primary},
		},
	}

	_, err := resolvePopTarget("/etc/passwd", repoRoot, pcfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not inside any mirrored")
}
