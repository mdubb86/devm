package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mdubb86/devm/internal/repohelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSubdirDiscovery_ProjectRootFromSubdir asserts that FindDevmYAML
// finds the project root when called from a deeply-nested subdir. This
// mirrors what every CLI command that previously did os.Getwd() should
// now be doing.
func TestSubdirDiscovery_ProjectRootFromSubdir(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "devm.yaml"), []byte("project:\n  name: t\n"), 0o644))
	sub := filepath.Join(root, "a", "b", "c")
	require.NoError(t, os.MkdirAll(sub, 0o755))

	got, err := repohelpers.FindDevmYAML(sub)
	require.NoError(t, err)
	assert.Equal(t, root, got)
}

// TestSubdirDiscovery_NoYAMLFailsLoud asserts that a non-project cwd
// fails with a clear error naming the cwd — the same failure mode
// every migrated command should surface.
func TestSubdirDiscovery_NoYAMLFailsLoud(t *testing.T) {
	dir := t.TempDir()
	_, err := repohelpers.FindDevmYAML(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not inside a devm project")
	assert.Contains(t, err.Error(), dir)
}
