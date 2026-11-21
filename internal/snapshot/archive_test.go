package snapshot

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewTimestamp_FormatIsUTCNoColon(t *testing.T) {
	ts := NewTimestamp(time.Date(2026, 3, 1, 12, 0, 5, 0, time.UTC))
	assert.Equal(t, "2026-03-01T1200-05", ts)
}

func TestNewTimestamp_ConvertsToUTC(t *testing.T) {
	// A non-UTC instant must render in UTC.
	loc, err := time.LoadLocation("America/Los_Angeles")
	require.NoError(t, err)
	ts := NewTimestamp(time.Date(2026, 3, 1, 5, 0, 5, 0, loc))
	assert.Equal(t, "2026-03-01T1300-05", ts)
}

func TestWriteReadMeta_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	m := Meta{
		TS:          "2026-03-01T1200-05",
		HookName:    "snapshot.pg-data",
		ArchiveName: "pg-data",
		Source:      SourceCLI,
		ExitCode:    0,
		DurationMs:  1234,
		SizeBytes:   42,
		Status:      StatusOk,
	}
	require.NoError(t, WriteMeta(dir, m.TS, m))

	got, err := ReadMeta(dir, m.TS)
	require.NoError(t, err)
	assert.Equal(t, m, got)
}

func TestWriteMeta_IndentedForHumans(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, WriteMeta(dir, "2026-01-01T0000-00", Meta{TS: "2026-01-01T0000-00", Source: SourceCron}))
	b, err := os.ReadFile(filepath.Join(dir, "2026-01-01T0000-00.meta.json"))
	require.NoError(t, err)
	// json.MarshalIndent uses "  " and newlines; check for one.
	assert.Contains(t, string(b), "\n  \"ts\":")
}

func TestReadMeta_MissingSidecarReturnsUnknownSource(t *testing.T) {
	dir := t.TempDir()
	m, err := ReadMeta(dir, "2026-03-01T1200-05")
	require.NoError(t, err)
	assert.Equal(t, SourceUnknown, m.Source)
	assert.Equal(t, Meta{Source: SourceUnknown}, m)
}

func TestReadMeta_EmptyModeTreatedAsStream(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"ts":"2026-11-17T1200-00","hook_name":"backup_pg","archive_name":"pg","source":"cron","exit_code":0,"duration_ms":1500,"size_bytes":2048}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "2026-11-17T1200-00.meta.json"), []byte(legacy), 0644))
	m, err := ReadMeta(dir, "2026-11-17T1200-00")
	require.NoError(t, err)
	assert.Equal(t, ModeStream, m.EffectiveMode())
	assert.Empty(t, m.CapturedPath)
	assert.Equal(t, StatusOk, m.Status) // zero value normalized to Ok at read
}

func TestWriteReadMeta_PathModeRoundTrip(t *testing.T) {
	dir := t.TempDir()
	in := Meta{
		TS: "2026-11-17T1300-00", HookName: "pre_backup_claude", ArchiveName: "claude",
		Source: SourceCLI, ExitCode: 0, DurationMs: 500, SizeBytes: 123,
		Mode: ModePath, CapturedPath: "/home/devm/.claude", Status: StatusOk,
	}
	require.NoError(t, WriteMeta(dir, in.TS, in))
	out, err := ReadMeta(dir, in.TS)
	require.NoError(t, err)
	assert.Equal(t, ModePath, out.Mode)
	assert.Equal(t, "/home/devm/.claude", out.CapturedPath)
	assert.Equal(t, StatusOk, out.Status)
}

func TestWriteReadMeta_PostFailedRoundTrips(t *testing.T) {
	dir := t.TempDir()
	in := Meta{TS: "2026-11-17T1400-00", Mode: ModePath, Status: StatusPostFailed, ArchiveName: "x"}
	require.NoError(t, WriteMeta(dir, in.TS, in))
	out, _ := ReadMeta(dir, in.TS)
	assert.Equal(t, StatusPostFailed, out.Status)
}

func TestWriteReadMeta_IsDirRoundTrips(t *testing.T) {
	dir := t.TempDir()
	in := Meta{
		TS: "2026-11-17T1500-00", ArchiveName: "claude",
		Mode: ModePath, CapturedPath: "/home/devm/.claude",
		IsDir: true, Status: StatusOk,
	}
	require.NoError(t, WriteMeta(dir, in.TS, in))
	out, err := ReadMeta(dir, in.TS)
	require.NoError(t, err)
	assert.True(t, out.IsDir)
}

func TestListTimestamps_FiltersByTimestampStem(t *testing.T) {
	dir := t.TempDir()
	stamps := []string{"2026-03-01T1200-05", "2026-03-02T0900-01", "2026-02-28T2359-59"}
	for _, s := range stamps {
		require.NoError(t, os.WriteFile(filepath.Join(dir, s+".zst"), []byte{0}, 0o644))
	}
	// A stray .zst with a non-timestamp stem must not count.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "garbage.zst"), []byte{0}, 0o644))
	// A .meta.json sidecar must not count.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "2026-03-01T1200-05.meta.json"), []byte("{}"), 0o644))

	got, err := ListTimestamps(dir)
	require.NoError(t, err)
	assert.ElementsMatch(t, stamps, got)
}

func TestListTimestamps_MissingDirIsEmpty(t *testing.T) {
	got, err := ListTimestamps(filepath.Join(t.TempDir(), "never"))
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestUpdateLatest_PointsToArchive(t *testing.T) {
	dir := t.TempDir()
	// Create the target archive so Readlink returns a valid path.
	ts := "2026-03-01T1200-05"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ts+".zst"), []byte("x"), 0644))

	require.NoError(t, UpdateLatest(dir, ts))

	target, err := os.Readlink(filepath.Join(dir, "latest"))
	require.NoError(t, err)
	assert.Equal(t, ts+".zst", target)
}

func TestUpdateLatest_OverwritesPrevious(t *testing.T) {
	dir := t.TempDir()
	for _, ts := range []string{"2026-03-01T1200-05", "2026-03-02T1200-05"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, ts+".zst"), []byte("x"), 0644))
		require.NoError(t, UpdateLatest(dir, ts))
	}
	target, err := os.Readlink(filepath.Join(dir, "latest"))
	require.NoError(t, err)
	assert.Equal(t, "2026-03-02T1200-05.zst", target)
}

// TestUpdateLatest_IsAtomic hammers the temp+rename path from two
// goroutines; the symlink must always resolve to one of the two
// candidates (never half-written, never missing).
func TestUpdateLatest_IsAtomic(t *testing.T) {
	dir := t.TempDir()
	tsA := "2026-03-01T1200-05"
	tsB := "2026-03-02T1200-05"
	for _, ts := range []string{tsA, tsB} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, ts+".zst"), []byte("x"), 0644))
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); _ = UpdateLatest(dir, tsA) }()
		go func() { defer wg.Done(); _ = UpdateLatest(dir, tsB) }()
	}
	wg.Wait()

	target, err := os.Readlink(filepath.Join(dir, "latest"))
	require.NoError(t, err)
	assert.Contains(t, []string{tsA + ".zst", tsB + ".zst"}, target)
}

func TestLatestTS_ReadsSymlink(t *testing.T) {
	dir := t.TempDir()
	ts := "2026-03-01T1200-05"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ts+".zst"), []byte("x"), 0644))
	require.NoError(t, UpdateLatest(dir, ts))

	got, err := LatestTS(dir)
	require.NoError(t, err)
	assert.Equal(t, ts, got)
}

func TestLatestTS_MissingReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	got, err := LatestTS(dir)
	require.NoError(t, err)
	assert.Equal(t, "", got)
}

// writeArchiveMeta plants both the archive placeholder and its sidecar
// so ListTimestamps enumerates the ts and ReadMeta returns m.Status.
func writeArchiveMeta(t *testing.T, dir, ts string, m Meta) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ts+".zst"), []byte("x"), 0o644))
	m.TS = ts
	require.NoError(t, WriteMeta(dir, ts, m))
}

func TestSelectLatest_PrefersOkOverPostFailed(t *testing.T) {
	dir := t.TempDir()
	writeArchiveMeta(t, dir, "2026-11-17T1000-00", Meta{Status: StatusOk})
	writeArchiveMeta(t, dir, "2026-11-17T1100-00", Meta{Status: StatusPostFailed})
	ts, err := SelectLatest(dir)
	require.NoError(t, err)
	assert.Equal(t, "2026-11-17T1000-00", ts)
}

func TestSelectLatest_FallsBackToPostFailedWhenNoOk(t *testing.T) {
	dir := t.TempDir()
	writeArchiveMeta(t, dir, "2026-11-17T1000-00", Meta{Status: StatusPostFailed})
	writeArchiveMeta(t, dir, "2026-11-17T1100-00", Meta{Status: StatusPostFailed})
	ts, err := SelectLatest(dir)
	require.NoError(t, err)
	assert.Equal(t, "2026-11-17T1100-00", ts)
}

func TestSelectLatest_EmptyDirReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	ts, err := SelectLatest(dir)
	require.NoError(t, err)
	assert.Equal(t, "", ts)
}

func TestSelectLatest_MissingDirReturnsEmpty(t *testing.T) {
	ts, err := SelectLatest(filepath.Join(t.TempDir(), "never"))
	require.NoError(t, err)
	assert.Equal(t, "", ts)
}

// A v0.31 archive has no "status" field; ReadMeta normalizes the zero
// value to StatusOk so SelectLatest treats it the same as a fresh ok.
func TestSelectLatest_V0_31SidecarCountsAsOk(t *testing.T) {
	dir := t.TempDir()
	// Hand-write a sidecar with no "status" field and a zst placeholder.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "2026-11-17T1000-00.zst"), []byte("x"), 0o644))
	legacy := `{"ts":"2026-11-17T1000-00","archive_name":"foo","source":"cron"}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "2026-11-17T1000-00.meta.json"), []byte(legacy), 0o644))
	// A post_failed archive after it must not win.
	writeArchiveMeta(t, dir, "2026-11-17T1100-00", Meta{Status: StatusPostFailed})

	ts, err := SelectLatest(dir)
	require.NoError(t, err)
	assert.Equal(t, "2026-11-17T1000-00", ts)
}

// Operator removed the archive out from under devm; the symlink
// remains pointing at nothing. LatestTS must return empty + nil.
func TestLatestTS_BrokenSymlinkReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	ts := "2026-03-01T1200-05"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ts+".zst"), []byte("x"), 0644))
	require.NoError(t, UpdateLatest(dir, ts))
	// Operator deletes the target archive.
	require.NoError(t, os.Remove(filepath.Join(dir, ts+".zst")))

	got, err := LatestTS(dir)
	require.NoError(t, err)
	assert.Equal(t, "", got)
}
