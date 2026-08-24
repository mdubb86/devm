package serviceapi

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureVMSymlink_Creates(t *testing.T) {
	cwd := t.TempDir()
	target := t.TempDir()
	require.NoError(t, EnsureVMSymlink(cwd, target))

	link := filepath.Join(cwd, ".vm")
	got, err := os.Readlink(link)
	require.NoError(t, err)
	assert.Equal(t, target, got)
}

func TestEnsureVMSymlink_Idempotent(t *testing.T) {
	cwd := t.TempDir()
	target := t.TempDir()
	require.NoError(t, EnsureVMSymlink(cwd, target))
	require.NoError(t, EnsureVMSymlink(cwd, target))

	link := filepath.Join(cwd, ".vm")
	got, err := os.Readlink(link)
	require.NoError(t, err)
	assert.Equal(t, target, got)
}

func TestEnsureVMSymlink_RefreshesStale(t *testing.T) {
	cwd := t.TempDir()
	stale := t.TempDir()
	fresh := t.TempDir()
	require.NoError(t, EnsureVMSymlink(cwd, stale))
	require.NoError(t, EnsureVMSymlink(cwd, fresh))

	got, err := os.Readlink(filepath.Join(cwd, ".vm"))
	require.NoError(t, err)
	assert.Equal(t, fresh, got)
}

func TestEnsureVMSymlink_SelfHealsDeletion(t *testing.T) {
	cwd := t.TempDir()
	target := t.TempDir()
	require.NoError(t, EnsureVMSymlink(cwd, target))
	require.NoError(t, os.Remove(filepath.Join(cwd, ".vm")))
	require.NoError(t, EnsureVMSymlink(cwd, target))

	_, err := os.Readlink(filepath.Join(cwd, ".vm"))
	assert.NoError(t, err)
}

func TestEnsureVMSymlink_RefusesNonSymlink(t *testing.T) {
	cwd := t.TempDir()
	target := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(cwd, ".vm"), 0755))

	err := EnsureVMSymlink(cwd, target)
	assert.Error(t, err)

	fi, statErr := os.Lstat(filepath.Join(cwd, ".vm"))
	require.NoError(t, statErr)
	assert.True(t, fi.IsDir(), ".vm should be left alone as a real dir")
}

func TestEnsureGitExclude_AppendsOnce(t *testing.T) {
	cwd := t.TempDir()
	require.NoError(t, exec.Command("git", "-C", cwd, "init", "-q").Run())

	require.NoError(t, EnsureGitExclude(cwd))
	require.NoError(t, EnsureGitExclude(cwd)) // idempotent

	body, err := os.ReadFile(filepath.Join(cwd, ".git", "info", "exclude"))
	require.NoError(t, err)
	count := strings.Count(string(body), "/.vm\n")
	assert.Equal(t, 1, count, "should append exactly once, got %d occurrences", count)
}

func TestEnsureGitExclude_NoGitDir_NoOp(t *testing.T) {
	cwd := t.TempDir() // no .git
	assert.NoError(t, EnsureGitExclude(cwd))
}
