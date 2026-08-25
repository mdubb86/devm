package repohelpers

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFindDevmYAML_CwdIsProjectRoot(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "devm.yaml"), []byte("project:\n  name: t\n"), 0o644))
	got, err := FindDevmYAML(dir)
	require.NoError(t, err)
	assert.Equal(t, dir, got)
}

func TestFindDevmYAML_CwdIsSubdir(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "devm.yaml"), []byte("project:\n  name: t\n"), 0o644))
	sub := filepath.Join(root, "src", "deep", "nested")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	got, err := FindDevmYAML(sub)
	require.NoError(t, err)
	assert.Equal(t, root, got)
}

func TestFindDevmYAML_NoYAMLAnywhere(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	_, err := FindDevmYAML(sub)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no devm.yaml found")
	assert.Contains(t, err.Error(), sub)
}

func TestFindDevmYAML_StopsAtFilesystemRoot(t *testing.T) {
	// Starting at "/" with no devm.yaml there must not loop forever.
	_, err := FindDevmYAML("/")
	require.Error(t, err)
}

func TestFindDevmYAML_HonorsFirstYAMLFound(t *testing.T) {
	// Nested projects: the innermost devm.yaml wins.
	outer := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outer, "devm.yaml"), []byte("project:\n  name: outer\n"), 0o644))
	inner := filepath.Join(outer, "sub")
	require.NoError(t, os.MkdirAll(inner, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(inner, "devm.yaml"), []byte("project:\n  name: inner\n"), 0o644))
	sub := filepath.Join(inner, "deeper")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	got, err := FindDevmYAML(sub)
	require.NoError(t, err)
	assert.Equal(t, inner, got, "innermost devm.yaml must win")
}
