package release

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pinCache writes a nudgeCache file at the user-default location for
// the test. Uses DEVM_NUDGE_CACHE_DIR so we don't litter the real
// ~/.cache/devm.
func pinCache(t *testing.T, c nudgeCache) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DEVM_NUDGE_CACHE_DIR", dir)
	path := filepath.Join(dir, nudgeCheckFileName)
	data, err := json.Marshal(c)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o644))
}

// useTempCacheDir scopes the nudge cache to a tempdir without
// pre-populating it. Used by "missing cache" / "stale + fetch"
// tests that exercise the write path.
func useTempCacheDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DEVM_NUDGE_CACHE_DIR", dir)
	return dir
}

func TestMaybeNudge_SuppressedByOptOutEnv(t *testing.T) {
	t.Setenv("DEVM_NO_UPDATE_CHECK", "1")
	pinCache(t, nudgeCache{CheckedAt: time.Now().Unix(), LatestTag: "9.9.9"})

	var buf bytes.Buffer
	MaybeNudge(context.Background(), &buf, "0.1.0", nil, nil)
	assert.Empty(t, buf.String(), "DEVM_NO_UPDATE_CHECK=1 must suppress nudge")
}

func TestMaybeNudge_SuppressedByCIEnv(t *testing.T) {
	t.Setenv("CI", "true")
	pinCache(t, nudgeCache{CheckedAt: time.Now().Unix(), LatestTag: "9.9.9"})

	var buf bytes.Buffer
	MaybeNudge(context.Background(), &buf, "0.1.0", nil, nil)
	assert.Empty(t, buf.String(), "CI=true must suppress nudge")
}

func TestMaybeNudge_SuppressedByDevVersion(t *testing.T) {
	pinCache(t, nudgeCache{CheckedAt: time.Now().Unix(), LatestTag: "9.9.9"})

	var buf bytes.Buffer
	MaybeNudge(context.Background(), &buf, "dev", nil, nil)
	assert.Empty(t, buf.String(), "currentVersion=dev must suppress nudge")
}

func TestMaybeNudge_SuppressedByEmptyVersion(t *testing.T) {
	pinCache(t, nudgeCache{CheckedAt: time.Now().Unix(), LatestTag: "9.9.9"})
	var buf bytes.Buffer
	MaybeNudge(context.Background(), &buf, "", nil, nil)
	assert.Empty(t, buf.String(), "empty currentVersion must suppress nudge")
}

func TestMaybeNudge_PrintsWhenCacheFreshAndNewer(t *testing.T) {
	pinCache(t, nudgeCache{CheckedAt: time.Now().Unix(), LatestTag: "0.2.0"})

	var buf bytes.Buffer
	MaybeNudge(context.Background(), &buf, "0.1.0", nil, nil)
	got := buf.String()
	assert.Contains(t, got, "devm v0.2.0 available")
	assert.Contains(t, got, "devm upgrade")
}

func TestMaybeNudge_SilentWhenCacheFreshAndCurrent(t *testing.T) {
	pinCache(t, nudgeCache{CheckedAt: time.Now().Unix(), LatestTag: "0.1.0"})

	var buf bytes.Buffer
	MaybeNudge(context.Background(), &buf, "0.1.0", nil, nil)
	assert.Empty(t, buf.String(), "matching version must not nudge")
}

func TestMaybeNudge_StaleCacheFetchesSyncWithSpinnerAndWritesCache(t *testing.T) {
	// Cache older than 7 days.
	dir := useTempCacheDir(t)
	require.NoError(t, writeNudgeCache(
		filepath.Join(dir, nudgeCheckFileName),
		nudgeCache{
			CheckedAt: time.Now().Add(-8 * 24 * time.Hour).Unix(),
			LatestTag: "0.0.9",
		},
	))

	fetcher := func(ctx context.Context) string { return "0.5.0" }

	var buf bytes.Buffer
	MaybeNudge(context.Background(), &buf, "0.1.0", fetcher, nil)
	got := buf.String()

	// Plain transcript reporter (non-TTY buf) emits "[devm] checking
	// for devm updates". Then the nudge line follows.
	assert.Contains(t, got, "checking for devm updates",
		"spinner/transcript must surface the in-progress fetch")
	assert.Contains(t, got, "devm v0.5.0 available",
		"after fetch, the nudge line must appear")

	// Cache should be refreshed on disk.
	c, err := readNudgeCache(filepath.Join(dir, nudgeCheckFileName))
	require.NoError(t, err)
	assert.Equal(t, "0.5.0", c.LatestTag)
	assert.WithinDuration(t, time.Now(), time.Unix(c.CheckedAt, 0), 5*time.Second)
}

func TestMaybeNudge_StaleCacheSilentIfFetcherReturnsEmpty(t *testing.T) {
	useTempCacheDir(t)
	fetcher := func(ctx context.Context) string { return "" }

	var buf bytes.Buffer
	MaybeNudge(context.Background(), &buf, "0.1.0", fetcher, nil)
	got := buf.String()
	// The spinner-start line is fine (it's transient UX), but no
	// nudge line should appear since fetcher returned nothing.
	assert.NotContains(t, got, "available")
}

func TestMaybeNudge_StaleCacheSilentIfLatestMatchesCurrent(t *testing.T) {
	useTempCacheDir(t)
	fetcher := func(ctx context.Context) string { return "0.1.0" }

	var buf bytes.Buffer
	MaybeNudge(context.Background(), &buf, "0.1.0", fetcher, nil)
	got := buf.String()
	assert.NotContains(t, got, "available",
		"no nudge when latest == currentVersion")
}

func TestMaybeNudge_StaleCacheWithNilFetcherIsNoop(t *testing.T) {
	pinCache(t, nudgeCache{
		CheckedAt: time.Now().Add(-30 * 24 * time.Hour).Unix(),
		LatestTag: "0.0.9",
	})

	var buf bytes.Buffer
	MaybeNudge(context.Background(), &buf, "0.1.0", nil, nil)
	assert.Empty(t, buf.String(), "nil fetcher → silent return")
}

func TestMaybeNudge_MissingCacheTriggersSyncFetch(t *testing.T) {
	useTempCacheDir(t)
	fetcher := func(ctx context.Context) string { return "0.5.0" }

	var buf bytes.Buffer
	MaybeNudge(context.Background(), &buf, "0.1.0", fetcher, nil)
	got := buf.String()
	assert.Contains(t, got, "checking for devm updates")
	assert.Contains(t, got, "devm v0.5.0 available")
}
