package snapshot

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mkArchive creates a stub <ts>.zst + <ts>.meta.json pair under dir.
func mkArchive(t *testing.T, dir, ts string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ts+".zst"), []byte("x"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ts+".meta.json"), []byte("{}"), 0644))
}

func TestPrune_KeepsNewestN(t *testing.T) {
	dir := t.TempDir()
	// Ten archives, deterministic filename order == timestamp order.
	var all []string
	for day := 1; day <= 10; day++ {
		ts := fmt.Sprintf("2026-03-%02dT1200-05", day)
		mkArchive(t, dir, ts)
		all = append(all, ts)
	}
	removed, err := Prune(dir, 3)
	require.NoError(t, err)

	sort.Strings(removed)
	expectedRemoved := all[:7] // oldest 7 removed
	sort.Strings(expectedRemoved)
	assert.Equal(t, expectedRemoved, removed)

	// Newest 3 archives + their sidecars survived.
	for _, ts := range all[7:] {
		assert.FileExists(t, filepath.Join(dir, ts+".zst"))
		assert.FileExists(t, filepath.Join(dir, ts+".meta.json"))
	}
	for _, ts := range all[:7] {
		_, err := os.Stat(filepath.Join(dir, ts+".zst"))
		assert.True(t, os.IsNotExist(err))
		_, err = os.Stat(filepath.Join(dir, ts+".meta.json"))
		assert.True(t, os.IsNotExist(err))
	}
}

func TestPrune_SortsByFilenameNotMtime(t *testing.T) {
	dir := t.TempDir()
	// Write the newest-by-ts last? No — write it first so the mtime
	// is older than the others. If Prune used mtime it would keep
	// the wrong files.
	tsNewest := "2026-12-31T2359-59"
	tsMid := "2026-06-15T1200-00"
	tsOldest := "2026-01-01T0000-00"

	mkArchive(t, dir, tsNewest)
	// Tiny bit of fs mtime separation by letting the fs move on.
	mkArchive(t, dir, tsOldest)
	mkArchive(t, dir, tsMid)

	removed, err := Prune(dir, 1)
	require.NoError(t, err)

	// tsNewest must survive; the other two gone.
	sort.Strings(removed)
	expected := []string{tsMid, tsOldest}
	sort.Strings(expected)
	assert.Equal(t, expected, removed)
	assert.FileExists(t, filepath.Join(dir, tsNewest+".zst"))
}

func TestPrune_KeepEqualsCountIsNoOp(t *testing.T) {
	dir := t.TempDir()
	for day := 1; day <= 3; day++ {
		mkArchive(t, dir, fmt.Sprintf("2026-03-%02dT1200-05", day))
	}
	removed, err := Prune(dir, 3)
	require.NoError(t, err)
	assert.Empty(t, removed)
}

func TestPrune_KeepGreaterThanCountIsNoOp(t *testing.T) {
	dir := t.TempDir()
	mkArchive(t, dir, "2026-03-01T1200-05")
	removed, err := Prune(dir, 10)
	require.NoError(t, err)
	assert.Empty(t, removed)
}

func TestPrune_RemovesSidecarWithArchive(t *testing.T) {
	dir := t.TempDir()
	mkArchive(t, dir, "2026-03-01T1200-05")
	mkArchive(t, dir, "2026-03-02T1200-05")
	_, err := Prune(dir, 1)
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, "2026-03-01T1200-05.meta.json"))
	assert.True(t, os.IsNotExist(err))
}

func TestPrune_ArchiveWithoutSidecarIsFine(t *testing.T) {
	dir := t.TempDir()
	// Archive missing a sidecar — prune must still remove it.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "2026-03-01T1200-05.zst"), []byte("x"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "2026-03-02T1200-05.zst"), []byte("x"), 0644))
	removed, err := Prune(dir, 1)
	require.NoError(t, err)
	assert.Equal(t, []string{"2026-03-01T1200-05"}, removed)
}

func TestPrune_RejectsNonPositiveKeep(t *testing.T) {
	dir := t.TempDir()
	mkArchive(t, dir, "2026-03-01T1200-05")
	_, err := Prune(dir, 0)
	require.Error(t, err)
	_, err = Prune(dir, -1)
	require.Error(t, err)
	// Archive must still exist — the call rejected, nothing deleted.
	assert.FileExists(t, filepath.Join(dir, "2026-03-01T1200-05.zst"))
}

func TestPrune_MissingDirReturnsEmpty(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nope")
	removed, err := Prune(dir, 3)
	require.NoError(t, err)
	assert.Empty(t, removed)
}

// A stray *.zst with a non-timestamp stem must not be treated as a
// snapshot — otherwise its stem can sort ahead of real archives
// (e.g. "garbage" > "2026-...") and "protect" itself while a real
// archive gets pruned.
func TestPrune_IgnoresMalformedNames(t *testing.T) {
	dir := t.TempDir()
	newest := "2026-03-02T1200-05"
	older := "2026-03-01T1200-05"
	mkArchive(t, dir, newest)
	mkArchive(t, dir, older)

	// Stray files that happen to end in .zst but are not archives.
	strays := []string{
		"garbage.zst",
		"some-random-file.zst",
		".zst",
	}
	for _, s := range strays {
		require.NoError(t, os.WriteFile(filepath.Join(dir, s), []byte("x"), 0644))
	}

	removed, err := Prune(dir, 1)
	require.NoError(t, err)

	// Only the older valid archive is removed.
	assert.Equal(t, []string{older}, removed)
	assert.FileExists(t, filepath.Join(dir, newest+".zst"))
	_, err = os.Stat(filepath.Join(dir, older+".zst"))
	assert.True(t, os.IsNotExist(err), "older archive should be removed")

	// Every stray survives untouched.
	for _, s := range strays {
		assert.FileExists(t, filepath.Join(dir, s), "stray %q should be left alone", s)
	}
}
