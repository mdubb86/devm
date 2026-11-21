package snapshot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Each line is a JSON object with ts, source, exit_code, reason.
// AppendFailure writes the raw line verbatim and ensures a trailing
// newline.

func TestAppendFailure_AppendsLineWithNewline(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, AppendFailure(dir, `{"ts":"2026-03-01T1200-05","source":"cli","exit_code":1,"reason":"x"}`))
	b, err := os.ReadFile(filepath.Join(dir, "failures.log"))
	require.NoError(t, err)
	assert.Equal(t, `{"ts":"2026-03-01T1200-05","source":"cli","exit_code":1,"reason":"x"}`+"\n", string(b))
}

func TestAppendFailure_AcceptsTrailingNewlineOnce(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, AppendFailure(dir, "line1\n"))
	require.NoError(t, AppendFailure(dir, "line2"))
	b, err := os.ReadFile(filepath.Join(dir, "failures.log"))
	require.NoError(t, err)
	assert.Equal(t, "line1\nline2\n", string(b))
}

func TestAppendFailure_TrimsWhenAboveThreshold(t *testing.T) {
	dir := t.TempDir()
	// Write identical lines until we cross the rotate threshold.
	// A rotate must have fired by then, so size sits at <= target +
	// one line.
	line := fmt.Sprintf(`{"ts":"2026-03-01T1200-05","source":"cli","exit_code":1,"reason":"%s"}`,
		strings.Repeat("x", 80))
	writes := (maxFailuresBytes / (len(line) + 1)) + 2
	for i := 0; i < writes; i++ {
		require.NoError(t, AppendFailure(dir, line))
	}
	b, err := os.ReadFile(filepath.Join(dir, "failures.log"))
	require.NoError(t, err)
	// Right after the trim-triggering write, size must sit near the
	// target. One line's slack is enough.
	assert.LessOrEqual(t, len(b), trimTargetBytes+len(line)+1,
		"expected trim to ~%dB; got %d bytes", trimTargetBytes, len(b))
	// Newest line must survive the trim.
	assert.True(t, strings.HasSuffix(string(b), line+"\n"),
		"newest line missing after trim")
}

func TestAppendFailure_StaysBoundedAcrossManyWrites(t *testing.T) {
	dir := t.TempDir()
	msg := fmt.Sprintf(`{"ts":"2026-03-01T1200-05","source":"cli","exit_code":1,"reason":"%s"}`,
		strings.Repeat("x", 80))
	for i := 0; i < 500; i++ {
		require.NoError(t, AppendFailure(dir, msg))
		b, err := os.ReadFile(filepath.Join(dir, "failures.log"))
		require.NoError(t, err)
		// After any write, size must stay below the rotate ceiling
		// + one line's slack.
		assert.LessOrEqual(t, len(b), maxFailuresBytes+len(msg)+1,
			"file grew unbounded at write %d: %d bytes", i, len(b))
	}
}

func TestAppendFailure_BelowThresholdLeavesAlone(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, AppendFailure(dir, "one"))
	require.NoError(t, AppendFailure(dir, "two"))
	b, err := os.ReadFile(filepath.Join(dir, "failures.log"))
	require.NoError(t, err)
	assert.Equal(t, "one\ntwo\n", string(b))
}

func TestRecentFailures_CountsInWindow(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	lines := []struct {
		at  time.Time
		msg string
	}{
		{now.Add(-2 * time.Hour), "old-1"},
		{now.Add(-90 * time.Minute), "old-2"},
		{now.Add(-30 * time.Minute), "recent-1"},
		{now.Add(-5 * time.Minute), "recent-2"},
		{now.Add(-1 * time.Second), "recent-3"},
	}
	for _, l := range lines {
		b, err := json.Marshal(map[string]any{
			"ts":        NewTimestamp(l.at),
			"source":    "cli",
			"exit_code": 1,
			"reason":    l.msg,
		})
		require.NoError(t, err)
		require.NoError(t, AppendFailure(dir, string(b)))
	}
	n, err := RecentFailures(dir, time.Hour)
	require.NoError(t, err)
	assert.Equal(t, 3, n)
}

func TestRecentFailures_MissingFileReturnsZero(t *testing.T) {
	dir := t.TempDir()
	n, err := RecentFailures(dir, time.Hour)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}

func TestRecentFailures_ReturnsErrorOnMalformedLine(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "failures.log"), []byte("not-json\n"), 0644))
	_, err := RecentFailures(dir, time.Hour)
	require.Error(t, err)
}
