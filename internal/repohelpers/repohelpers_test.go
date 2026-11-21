package repohelpers

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeRepoWithOrigin(t *testing.T, origin string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, exec.Command("git", "-C", dir, "init", "-q").Run())
	require.NoError(t, exec.Command("git", "-C", dir, "remote", "add", "origin", origin).Run())
	return dir
}

func TestDeriveRepoURL_OK(t *testing.T) {
	dir := makeRepoWithOrigin(t, "https://github.com/me/foo.git")
	got, err := DeriveRepoURL(dir)
	require.NoError(t, err)
	assert.Equal(t, "https://github.com/me/foo.git", got)
}

func TestDeriveRepoURL_NotAGitRepo(t *testing.T) {
	dir := t.TempDir()
	_, err := DeriveRepoURL(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "run inside a git repo")
}

func TestDeriveRepoURL_NoOriginRemote(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, exec.Command("git", "-C", dir, "init", "-q").Run())
	_, err := DeriveRepoURL(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no `origin` remote")
}

func TestDeriveRepoURL_TrimTrailing(t *testing.T) {
	dir := makeRepoWithOrigin(t, "git@github.com:me/foo.git")
	got, err := DeriveRepoURL(dir)
	require.NoError(t, err)
	assert.False(t, strings.HasSuffix(got, "\n"))
	assert.Equal(t, "git@github.com:me/foo.git", got)
}

func TestPrimaryVolumeName(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"/Users/me/projects/sewtrue", "sewtrue"},
		{"/Users/me/projects/sewtrue/", "sewtrue"},
		{"/tmp/foo", "foo"},
	}
	for _, tc := range cases {
		got := PrimaryVolumeName(tc.in)
		assert.Equal(t, tc.want, got, "input %q", tc.in)
	}
}

func TestPrimaryVolumeName_UsesFilepathBase(t *testing.T) {
	// Sanity: our helper is just filepath.Base semantics.
	got := PrimaryVolumeName(filepath.Join("/x", "y", "z"))
	assert.Equal(t, "z", got)
}
