// internal/approve/snapshot_test.go
package approve

import (
	"path/filepath"
	"testing"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	return NewStore(identity.Config{Name: "devm-test-approve"}), dir
}

func TestStore_ReadMissingReturnsNotFound(t *testing.T) {
	s, _ := newTestStore(t)
	_, ok, err := s.Read("proj-1")
	require.NoError(t, err)
	assert.False(t, ok, "no snapshot yet must return ok=false")
}

func TestStore_WriteReadRoundtripUserBothFiles(t *testing.T) {
	s, _ := newTestStore(t)
	dv := []byte("project:\n  name: p\n")
	me := []byte("env:\n  DEBUG: 1\n")
	require.NoError(t, s.Write("proj-1", dv, me, "user"))
	snap, ok, err := s.Read("proj-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, dv, snap.DevmYAML)
	assert.Equal(t, me, snap.MeYAML)
	assert.Equal(t, "user", snap.Manifest.Source)
	assert.False(t, snap.Manifest.Timestamp.IsZero())
}

func TestStore_WriteReadRoundtripNoMeYAML(t *testing.T) {
	s, _ := newTestStore(t)
	dv := []byte("project:\n  name: p\n")
	require.NoError(t, s.Write("proj-1", dv, nil, "guest"))
	snap, ok, err := s.Read("proj-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, dv, snap.DevmYAML)
	assert.Nil(t, snap.MeYAML)
	assert.Equal(t, "guest", snap.Manifest.Source)
}

func TestStore_WriteIsAtomic(t *testing.T) {
	// Write twice; the second must fully replace the first (no torn
	// hybrid). Assert via re-Read matches the second write.
	s, root := newTestStore(t)
	require.NoError(t, s.Write("proj-1", []byte("first"), []byte("first-me"), "user"))
	require.NoError(t, s.Write("proj-1", []byte("second"), nil, "guest"))
	snap, ok, err := s.Read("proj-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, []byte("second"), snap.DevmYAML)
	assert.Nil(t, snap.MeYAML, "second write with no meYAML must remove the old me file")
	assert.NoFileExists(t, filepath.Join(root, "Library", "Application Support", "devm-test-approve", "proj-1", "approved-snapshot", "devm.me.yaml"))
}

func TestHashFile_DeterministicAndDifferent(t *testing.T) {
	assert.Equal(t, HashFile([]byte("x")), HashFile([]byte("x")))
	assert.NotEqual(t, HashFile([]byte("x")), HashFile([]byte("y")))
	assert.NotEqual(t, HashFile(nil), HashFile([]byte("")), "nil vs empty must differ (nil means 'file absent', empty means 'zero-byte file')")
}

func TestStore_WriteRejectsEmptyProjectID(t *testing.T) {
	s, _ := newTestStore(t)
	err := s.Write("", []byte("x"), nil, "user")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "projectID")
}
