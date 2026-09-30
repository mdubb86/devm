package serviceapi

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------- parseMutagenMonitorLine ----------

func TestParseMutagenMonitorLine(t *testing.T) {
	name, status, ok := parseMutagenMonitorLine("devm-proj-a-repo|Watching")
	require.True(t, ok)
	assert.Equal(t, "devm-proj-a-repo", name)
	assert.Equal(t, "Watching", status)
}

func TestParseMutagenMonitorLine_RejectsMalformed(t *testing.T) {
	for _, line := range []string{"", "no-pipe-here", "|Watching", "devm-proj-a-repo|", mutagenMonitorTickMarker} {
		_, _, ok := parseMutagenMonitorLine(line)
		assert.False(t, ok, "expected %q to be rejected", line)
	}
}

// ---------- sessionNameToProject ----------

func TestSessionNameToProject_LongestPrefixMatch(t *testing.T) {
	cache := NewStateCache()
	cache.SetMacCwd("foo", "/foo")
	cache.SetMacCwd("foo-bar", "/foo-bar")

	project, ok := sessionNameToProject(cache, SessionName("foo-bar", "repo"))
	require.True(t, ok)
	assert.Equal(t, "foo-bar", project)

	project, ok = sessionNameToProject(cache, SessionName("foo", "repo"))
	require.True(t, ok)
	assert.Equal(t, "foo", project)
}

func TestSessionNameToProject_UnknownProjectFails(t *testing.T) {
	cache := NewStateCache()
	cache.SetMacCwd("proj-a", "/a")

	_, ok := sessionNameToProject(cache, SessionName("proj-b", "repo"))
	assert.False(t, ok)
}

// ---------- statusToMutagenHealth ----------

func TestStatusToMutagenHealth_KnownHealthyStatuses(t *testing.T) {
	for _, status := range []string{"Watching", "Scanning", "StagingBeta", "ConnectingAlpha", "ConnectingBeta"} {
		assert.Equal(t, MutagenOK, statusToMutagenHealth(status).Status, "status %q should be healthy", status)
	}
}

func TestStatusToMutagenHealth_DisconnectedAndUnknownAreUnhealthy(t *testing.T) {
	for _, status := range []string{"Disconnected", "HaltedOnConnectionError", "SomeFutureStatus", ""} {
		assert.Equal(t, MutagenDead, statusToMutagenHealth(status).Status, "status %q should be unhealthy", status)
	}
}

// ---------- subscribeMutagenMonitorFromReader ----------

// writeMutagenMonitorTick writes one tick's session lines followed by
// the tick marker, in the exact shape mutagenMonitorTemplate renders.
func writeMutagenMonitorTick(t *testing.T, w io.Writer, lines ...string) {
	t.Helper()
	for _, l := range lines {
		fmt.Fprintf(w, "%s\n", l)
	}
	fmt.Fprintf(w, "%s\n", mutagenMonitorTickMarker)
}

// TestSubscribeMutagenMonitor_PerTickDiffUpdatesCache pins the core
// per-tick-diff behavior the spike (task-2-report.md finding 5)
// forced onto this design: mutagen re-emits every session's line on
// every tick, so the subscriber must diff the full table per tick
// against the previous tick's table, writing only real deltas rather
// than treating every re-emitted line as its own transition.
func TestSubscribeMutagenMonitor_PerTickDiffUpdatesCache(t *testing.T) {
	cache := NewStateCache()
	cache.SetMacCwd("proj-a", "/a")
	cache.SetMacCwd("proj-b", "/b")

	reader, writer := io.Pipe()
	done := make(chan struct{})
	go func() {
		subscribeMutagenMonitorFromReader(context.Background(), reader, cache)
		close(done)
	}()

	// Tick 1: both sessions healthy — first-ever observation, so both
	// get written even though "previous" started empty.
	writeMutagenMonitorTick(t, writer,
		SessionName("proj-a", "repo")+"|Watching",
		SessionName("proj-b", "repo")+"|Watching",
	)
	require.Eventually(t, func() bool {
		rowA, _ := cache.ProjectRow("proj-a")
		rowB, _ := cache.ProjectRow("proj-b")
		return rowA.MutagenHealth.Status == MutagenOK && rowB.MutagenHealth.Status == MutagenOK
	}, 2*time.Second, 10*time.Millisecond)

	// Poison proj-a's cached health with a value a correct re-write of
	// "Watching" would clear (Watching maps to MutagenOK, never
	// MutagenDead). proj-a's status does NOT change in tick 2 below —
	// if the subscriber wrongly rewrote every re-emitted line instead
	// of diffing, this poisoned value would be clobbered back to
	// MutagenOK and the assertion below would fail.
	cache.SetMutagenHealth("proj-a", MutagenHealth{Status: MutagenDead})

	// Tick 2: only proj-b transitions, to Disconnected.
	writeMutagenMonitorTick(t, writer,
		SessionName("proj-a", "repo")+"|Watching",
		SessionName("proj-b", "repo")+"|Disconnected",
	)
	require.Eventually(t, func() bool {
		rowB, _ := cache.ProjectRow("proj-b")
		return rowB.MutagenHealth.Status == MutagenDead
	}, 2*time.Second, 10*time.Millisecond)

	rowA, _ := cache.ProjectRow("proj-a")
	assert.Equal(t, MutagenDead, rowA.MutagenHealth.Status,
		"proj-a's unchanged session must not be rewritten by tick 2's diff")

	require.NoError(t, writer.Close())
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("subscribeMutagenMonitorFromReader did not return after writer close")
	}
}

// TestSubscribeMutagenMonitor_UnknownStatusMapsToUnhealthy feeds a
// status value outside the observed enum (task-2-report.md: only
// Watching, Disconnected, ConnectingAlpha, ConnectingBeta, Scanning,
// StagingBeta were confirmed live; HaltedOnConnectionError specifically
// was never observed) and asserts it surfaces as unhealthy rather than
// silently passing as fine.
func TestSubscribeMutagenMonitor_UnknownStatusMapsToUnhealthy(t *testing.T) {
	cache := NewStateCache()
	cache.SetMacCwd("proj-a", "/a")

	reader, writer := io.Pipe()
	done := make(chan struct{})
	go func() {
		subscribeMutagenMonitorFromReader(context.Background(), reader, cache)
		close(done)
	}()

	writeMutagenMonitorTick(t, writer, SessionName("proj-a", "repo")+"|SomeNeverSeenStatus")

	require.Eventually(t, func() bool {
		row, _ := cache.ProjectRow("proj-a")
		return row.MutagenHealth.Status == MutagenDead
	}, 2*time.Second, 10*time.Millisecond)

	require.NoError(t, writer.Close())
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("subscribeMutagenMonitorFromReader did not return after writer close")
	}
}

// TestSubscribeMutagenMonitor_HandlesReaderClose feeds a bounded
// reader that reaches EOF on its own (no explicit close call needed)
// and asserts the subscriber returns promptly instead of hanging —
// the failure mode -l/--long's bare-\r spinner mode would have hit
// with a \n-splitting bufio.Scanner (task-2-report.md finding 2).
func TestSubscribeMutagenMonitor_HandlesReaderClose(t *testing.T) {
	cache := NewStateCache()
	cache.SetMacCwd("proj-a", "/a")

	body := SessionName("proj-a", "repo") + "|Watching\n" + mutagenMonitorTickMarker + "\n"
	r := strings.NewReader(body)

	done := make(chan struct{})
	go func() {
		subscribeMutagenMonitorFromReader(context.Background(), r, cache)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("subscribeMutagenMonitorFromReader did not return after reader EOF")
	}

	row, _ := cache.ProjectRow("proj-a")
	assert.Equal(t, MutagenOK, row.MutagenHealth.Status)
}
