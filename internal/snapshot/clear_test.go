package snapshot

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClear_RemovesArchivesAndSidecars(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "entry")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	for _, ts := range []string{"2026-11-17T1000-00", "2026-11-17T1100-00"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, ts+".zst"), []byte("x"), 0o644))
		require.NoError(t, WriteMeta(dir, ts, Meta{TS: ts, Mode: ModePath}))
	}
	require.NoError(t, UpdateLatest(dir, "2026-11-17T1100-00"))
	require.NoError(t, AppendFailure(dir, `{"ts":"2026-11-17T1200-00"}`))
	// Simulate a crashed-write leftover — Clear must take it too so the
	// dir is actually empty afterward.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "2026-11-17T1200-00.0.tmp"), []byte("x"), 0o644))

	n, err := clearDir(dir)
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	// The entry dir itself is removed so DiscoverOrphans does not
	// surface the swept name as an orphan on the next scan.
	_, err = os.Stat(dir)
	assert.ErrorIs(t, err, fs.ErrNotExist)
}

func TestClear_RemovesEntryDirItself(t *testing.T) {
	// Seed a parent with a single entry dir, run Clear, and confirm the
	// dir is gone so a parent-dir scan (the shape DiscoverOrphans uses)
	// surfaces nothing. Guards the integration gap where an empty
	// entry dir still registered as an orphan.
	parent := t.TempDir()
	dir := filepath.Join(parent, "node_modules")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	ts := "2026-11-17T1000-00"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ts+".zst"), []byte("x"), 0o644))
	require.NoError(t, WriteMeta(dir, ts, Meta{TS: ts, Mode: ModePath}))

	n, err := clearDir(dir)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	_, err = os.Stat(dir)
	assert.ErrorIs(t, err, fs.ErrNotExist)

	ents, err := os.ReadDir(parent)
	require.NoError(t, err)
	assert.Empty(t, ents, "parent should hold no entry dir after clear")
}

func TestClear_MissingDirIsNoop(t *testing.T) {
	// Nested path that doesn't exist — Clear should return (0, nil)
	// so operators can idempotently clear an entry that never captured.
	dir := filepath.Join(t.TempDir(), "never", "captured")
	n, err := clearDir(dir)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}

func TestClear_CountsOnlyZstArchives(t *testing.T) {
	// Only *.zst count toward the returned number; the other files are
	// removed but do not inflate the "N archive(s)" operators see.
	dir := filepath.Join(t.TempDir(), "entry")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	ts := "2026-11-17T1000-00"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ts+".zst"), []byte("x"), 0o644))
	require.NoError(t, WriteMeta(dir, ts, Meta{TS: ts}))
	require.NoError(t, AppendFailure(dir, `{"ts":"`+ts+`"}`))

	n, err := clearDir(dir)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
}
