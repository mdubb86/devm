package serviceapi

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCwdRegistry_AddAndRead(t *testing.T) {
	cfg := identity.Prod
	t.Setenv("HOME", t.TempDir())

	require.NoError(t, AddCwdAlias(cfg, "shelfmates", "/Users/x/code/shelfmates"))
	require.NoError(t, AddCwdAlias(cfg, "shelfmates", "/Users/x/code/shelfmates-wt"))

	got, err := ReadCwdAliases(cfg, "shelfmates")
	require.NoError(t, err)
	assert.Equal(t, []string{"/Users/x/code/shelfmates", "/Users/x/code/shelfmates-wt"}, got)
}

func TestCwdRegistry_AddIsIdempotent(t *testing.T) {
	cfg := identity.Prod
	t.Setenv("HOME", t.TempDir())

	require.NoError(t, AddCwdAlias(cfg, "p", "/x"))
	require.NoError(t, AddCwdAlias(cfg, "p", "/x"))

	got, _ := ReadCwdAliases(cfg, "p")
	assert.Equal(t, []string{"/x"}, got)
}

func TestCwdRegistry_ReadAbsentReturnsNil(t *testing.T) {
	cfg := identity.Prod
	t.Setenv("HOME", t.TempDir())

	got, err := ReadCwdAliases(cfg, "never-registered")
	assert.NoError(t, err)
	assert.Nil(t, got)
}

func TestCwdRegistry_FindProjectByCwd(t *testing.T) {
	cfg := identity.Prod
	t.Setenv("HOME", t.TempDir())

	require.NoError(t, AddCwdAlias(cfg, "alpha", "/Users/x/alpha"))
	require.NoError(t, AddCwdAlias(cfg, "beta", "/Users/x/beta"))

	name, ok, err := FindProjectByCwd(cfg, "/Users/x/beta")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "beta", name)

	name, ok, err = FindProjectByCwd(cfg, "/Users/x/unknown")
	assert.NoError(t, err)
	assert.False(t, ok)
	assert.Empty(t, name)
}

func TestCwdRegistry_FindProjectByCwd_WalksUp(t *testing.T) {
	cfg := identity.Prod
	t.Setenv("HOME", t.TempDir())

	require.NoError(t, AddCwdAlias(cfg, "proj", "/Users/x/proj"))

	name, ok, err := FindProjectByCwd(cfg, "/Users/x/proj/subdir/deeper")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "proj", name)

	name, ok, err = FindProjectByCwd(cfg, "/Users/x/other/sub")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Empty(t, name)

	name, ok, err = FindProjectByCwd(cfg, "/Users/x/proj")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "proj", name)
}

func TestCwdRegistry_StateDirLayout(t *testing.T) {
	cfg := identity.Prod
	t.Setenv("HOME", "/some/home")
	got := stateDirForProject(cfg, "myproj")
	assert.Equal(t, filepath.Join("/some/home", "Library", "Application Support", "devm", "myproj"), got)
}

// Ensure atomic write: crash between .tmp and rename leaves the OLD file intact.
func TestCwdRegistry_WriteIsAtomic(t *testing.T) {
	cfg := identity.Prod
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, AddCwdAlias(cfg, "p", "/x"))
	// Simulate half-written .tmp by leaving one behind; a subsequent read must still succeed.
	stateDir := stateDirForProject(cfg, "p")
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "cwds.json.tmp"), []byte("garbage"), 0o644))
	got, err := ReadCwdAliases(cfg, "p")
	require.NoError(t, err)
	assert.Equal(t, []string{"/x"}, got)
}
