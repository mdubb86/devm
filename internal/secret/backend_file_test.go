package secret

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileBackend_SetGetRoundtrip(t *testing.T) {
	b := NewFileBackend(t.TempDir())
	require.NoError(t, b.Set("proj/api_key", "s3cret"))
	got, err := b.Get("proj/api_key")
	require.NoError(t, err)
	assert.Equal(t, "s3cret", got)
}

// TestFileBackend_GetMissing pins that a missing account returns
// ErrNotFound — every caller distinguishes "no such secret" from
// "storage error" using errors.Is(err, ErrNotFound).
func TestFileBackend_GetMissing(t *testing.T) {
	b := NewFileBackend(t.TempDir())
	_, err := b.Get("proj/missing")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNotFound), "want ErrNotFound, got %v", err)
}

// TestFileBackend_SetOverwrites pins that Set replaces the existing
// value at that account (matches the keychain backend's semantic).
func TestFileBackend_SetOverwrites(t *testing.T) {
	b := NewFileBackend(t.TempDir())
	require.NoError(t, b.Set("proj/k", "first"))
	require.NoError(t, b.Set("proj/k", "second"))
	got, err := b.Get("proj/k")
	require.NoError(t, err)
	assert.Equal(t, "second", got)
}

// TestFileBackend_List returns leaf names under the given projectID —
// the same "projectID/leaf" contract the keychain backend has, so
// callers upstream don't change.
func TestFileBackend_List(t *testing.T) {
	b := NewFileBackend(t.TempDir())
	require.NoError(t, b.Set("proj/a", "1"))
	require.NoError(t, b.Set("proj/b", "2"))
	require.NoError(t, b.Set("other/c", "3"))
	names, err := b.List("proj")
	require.NoError(t, err)
	sort.Strings(names)
	assert.Equal(t, []string{"a", "b"}, names)
}

// TestFileBackend_ListMissingProject returns nil, nil for a project
// with no secrets (matches keychain's "no rows" behavior).
func TestFileBackend_ListMissingProject(t *testing.T) {
	b := NewFileBackend(t.TempDir())
	names, err := b.List("nothing-here")
	require.NoError(t, err)
	assert.Empty(t, names)
}

func TestFileBackend_Delete(t *testing.T) {
	b := NewFileBackend(t.TempDir())
	require.NoError(t, b.Set("proj/k", "v"))
	require.NoError(t, b.Delete("proj/k"))
	_, err := b.Get("proj/k")
	assert.True(t, errors.Is(err, ErrNotFound))
}

func TestFileBackend_DeleteMissing(t *testing.T) {
	b := NewFileBackend(t.TempDir())
	err := b.Delete("proj/missing")
	assert.True(t, errors.Is(err, ErrNotFound))
}

// TestFileBackend_FileModes pins the on-disk perms — secrets are
// 0600 files under a 0700 root. FileVault covers at-rest encryption;
// these modes prevent read from other user accounts on the same Mac.
func TestFileBackend_FileModes(t *testing.T) {
	root := t.TempDir()
	b := NewFileBackend(root)
	require.NoError(t, b.Set("proj/k", "v"))

	// Project dir
	pdir, err := os.Stat(filepath.Join(root, "proj"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), pdir.Mode().Perm(), "project dir mode")

	// Secret file
	f, err := os.Stat(filepath.Join(root, "proj", "k"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), f.Mode().Perm(), "secret file mode")
}

// TestFileBackend_AtomicWrite_NoScratchLeftBehind pins that the
// tempfile+rename write path doesn't leave .tmp-* files in the dir
// after a successful Set — a stray one would list-poison.
func TestFileBackend_AtomicWrite_NoScratchLeftBehind(t *testing.T) {
	root := t.TempDir()
	b := NewFileBackend(root)
	require.NoError(t, b.Set("proj/k", "v"))
	entries, err := os.ReadDir(filepath.Join(root, "proj"))
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), ".tmp-", "atomic-write scratch left behind: %s", e.Name())
	}
}

// TestFileBackend_ListSkipsScratchFiles pins that if a Set is
// interrupted mid-write and leaves a .tmp-* orphan, List doesn't
// surface it as if it were a real secret.
func TestFileBackend_ListSkipsScratchFiles(t *testing.T) {
	root := t.TempDir()
	b := NewFileBackend(root)
	require.NoError(t, b.Set("proj/real", "v"))
	// Simulate an interrupted write: leave a scratch file behind.
	require.NoError(t, os.WriteFile(filepath.Join(root, "proj", ".tmp-orphan"), []byte("x"), 0o600))
	names, err := b.List("proj")
	require.NoError(t, err)
	assert.Equal(t, []string{"real"}, names)
}

// TestFileBackend_RejectsPathEscapes pins that an account name
// containing "../" (or an empty segment, or a NUL) can't write
// outside the root. Belt-and-suspenders — accounts come from
// user-authored devm.yaml, but the file backend is the only thing
// enforcing the containment.
func TestFileBackend_RejectsPathEscapes(t *testing.T) {
	b := NewFileBackend(t.TempDir())
	bad := []string{
		"..",
		"../evil",
		"proj/..",
		"proj/../evil",
		"",
		"proj/",
		"/proj/k",
		"proj\x00/k",
	}
	for _, name := range bad {
		t.Run(name, func(t *testing.T) {
			assert.Error(t, b.Set(name, "x"), "Set should reject %q", name)
		})
	}
}

// TestFileBackend_RejectsScratchPrefix pins that user-authored
// account names can't collide with the atomic-write scratch prefix —
// otherwise a legitimate secret named `.tmp-foo` would silently
// disappear from List.
func TestFileBackend_RejectsScratchPrefix(t *testing.T) {
	b := NewFileBackend(t.TempDir())
	assert.Error(t, b.Set("proj/.tmp-foo", "x"))
}
