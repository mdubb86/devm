package serviceapi

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProjectVolumesDir_ComputesPath(t *testing.T) {
	// Use a temp HOME so we don't touch the real ~/Library.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	cfg := identity.Config{Name: "devm-test"}
	got := projectVolumesDir(cfg, "myproj")
	assert.Equal(t,
		filepath.Join(tmp, "Library", "Application Support", "devm-test", "volumes", "myproj"),
		got)
}

func TestEnsureVolumeMacDir_CreatesFreshDir_ReportsEmpty(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	cfg := identity.Config{Name: "devm-test"}
	path, wasEmpty, err := ensureVolumeMacDir(cfg, "myproj", "pg-data")
	require.NoError(t, err)
	assert.True(t, wasEmpty, "freshly-created dir must be reported empty")

	// Path exists with mode 0700.
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
	assert.Equal(t, os.FileMode(0700), info.Mode().Perm())
}

func TestEnsureVolumeMacDir_ReportsNonEmpty(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	cfg := identity.Config{Name: "devm-test"}
	path, _, err := ensureVolumeMacDir(cfg, "myproj", "pg-data")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(path, "PG_VERSION"), []byte("14"), 0644))

	_, wasEmpty, err := ensureVolumeMacDir(cfg, "myproj", "pg-data")
	require.NoError(t, err)
	assert.False(t, wasEmpty, "dir containing files must be reported non-empty")
}

func TestEnsureVolumeMacDir_IdempotentOnRepeat(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	cfg := identity.Config{Name: "devm-test"}
	p1, _, err := ensureVolumeMacDir(cfg, "myproj", "pg-data")
	require.NoError(t, err)
	p2, _, err := ensureVolumeMacDir(cfg, "myproj", "pg-data")
	require.NoError(t, err)
	assert.Equal(t, p1, p2)
}
