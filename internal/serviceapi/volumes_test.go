package serviceapi

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProjectMirrorRoot_Path(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	cfg := identity.Config{Name: "devm-test"}
	got := projectMirrorRoot(cfg, "myproj")
	assert.Equal(t,
		filepath.Join(tmp, "Library", "Application Support", "devm-test", "myproj"),
		got)
}

func TestMirrorMacDir_Path(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	cfg := identity.Config{Name: "devm-test"}
	got := mirrorMacDir(cfg, "myproj", "pg-data")
	assert.Equal(t,
		filepath.Join(tmp, "Library", "Application Support", "devm-test", "myproj", "pg-data"),
		got)
}

func TestEnsureMirrorDir_CreatesDirAndReportsEmpty(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	cfg := identity.Config{Name: "devm-test"}
	path, wasEmpty, err := ensureMirrorDir(cfg, "myproj", "pg-data")
	require.NoError(t, err)
	assert.True(t, wasEmpty, "freshly-created dir must be reported empty")

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
	assert.Equal(t, os.FileMode(0700), info.Mode().Perm())
}

func TestEnsureMirrorDir_ExistingDirWithFiles_ReportsNonEmpty(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	cfg := identity.Config{Name: "devm-test"}
	path, _, err := ensureMirrorDir(cfg, "myproj", "pg-data")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(path, "PG_VERSION"), []byte("14"), 0644))

	_, wasEmpty, err := ensureMirrorDir(cfg, "myproj", "pg-data")
	require.NoError(t, err)
	assert.False(t, wasEmpty, "dir containing files must be reported non-empty")
}

func TestEnsureMirrorDir_ExistingEmptyDir_ReportsEmpty(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	cfg := identity.Config{Name: "devm-test"}
	p1, _, err := ensureMirrorDir(cfg, "myproj", "pg-data")
	require.NoError(t, err)
	p2, wasEmpty, err := ensureMirrorDir(cfg, "myproj", "pg-data")
	require.NoError(t, err)
	assert.Equal(t, p1, p2)
	assert.True(t, wasEmpty, "pre-existing empty dir must still be reported empty")
}
