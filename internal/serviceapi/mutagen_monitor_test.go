package serviceapi

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mdubb86/devm/internal/identity"
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

// TestStatusToMutagenHealth_KnownHealthyStatuses covers all ten
// non-failure values of the real synchronization.Status enum
// (task-9-review.md Finding 1) — every ordinary phase of a sync
// cycle, not just the five Task 9 originally allowlisted.
func TestStatusToMutagenHealth_KnownHealthyStatuses(t *testing.T) {
	for _, status := range []string{
		"Watching", "Scanning", "StagingBeta", "ConnectingAlpha", "ConnectingBeta",
		"StagingAlpha", "Reconciling", "Saving", "Transitioning", "WaitingForRescan",
	} {
		assert.Equal(t, MutagenOK, statusToMutagenHealth(status).Status, "status %q should be healthy", status)
	}
}

// TestStatusToMutagenHealth_FailedStatusesAreUnhealthy covers all
// four genuine failure values in mutagenFailedStatuses.
func TestStatusToMutagenHealth_FailedStatusesAreUnhealthy(t *testing.T) {
	for _, status := range []string{
		"Disconnected", "HaltedOnRootEmptied", "HaltedOnRootDeletion", "HaltedOnRootTypeChange",
	} {
		assert.Equal(t, MutagenDead, statusToMutagenHealth(status).Status, "status %q should be unhealthy", status)
	}
}

// TestStatusToMutagenHealth_UnknownStatusFailsOpenToHealthy pins the
// denylist's core behavior change from Task 9's original allowlist: a
// status outside both mutagenFailedStatuses and
// mutagenKnownHealthyStatuses maps to MutagenOK, not MutagenDead —
// see TestSubscribeMutagenMonitor_UnknownStatusLoggedOnce for the
// accompanying one-time warning.
func TestStatusToMutagenHealth_UnknownStatusFailsOpenToHealthy(t *testing.T) {
	for _, status := range []string{"HaltedOnConnectionError", "SomeFutureStatus", ""} {
		assert.Equal(t, MutagenOK, statusToMutagenHealth(status).Status, "unrecognized status %q should fail open to healthy", status)
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

// TestSubscribeMutagenMonitor_UnknownStatusMapsToHealthy feeds a
// status value outside both mutagenFailedStatuses and
// mutagenKnownHealthyStatuses through the full subscriber pipeline and
// asserts it fails open to healthy (denylist semantics), not
// unhealthy — see TestSubscribeMutagenMonitor_UnknownStatusLoggedOnce
// for the accompanying one-time warning this should also produce.
func TestSubscribeMutagenMonitor_UnknownStatusMapsToHealthy(t *testing.T) {
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
		return row.MutagenHealth.Status == MutagenOK
	}, 2*time.Second, 10*time.Millisecond)

	require.NoError(t, writer.Close())
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("subscribeMutagenMonitorFromReader did not return after writer close")
	}
}

// TestSubscribeMutagenMonitor_UnknownStatusLoggedOnce proves the
// per-status dedupe in statusToMutagenHealth: the same unrecognized
// status, observed by two different sessions and then again after an
// intervening transition (three appearances total), produces exactly
// one daemonlog.Warnf line — not one per occurrence, which would
// flood the log if mutagen started emitting an unrecognized value on
// every tick.
func TestSubscribeMutagenMonitor_UnknownStatusLoggedOnce(t *testing.T) {
	const unknownStatus = "TestUnknownStatusLoggedOnce_NeverARealMutagenStatus"

	cache := NewStateCache()
	cache.SetMacCwd("proj-a", "/a")
	cache.SetMacCwd("proj-b", "/b")

	origStderr := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = origStderr })

	reader, writer := io.Pipe()
	done := make(chan struct{})
	go func() {
		subscribeMutagenMonitorFromReader(context.Background(), reader, cache)
		close(done)
	}()

	// Tick 1: both sessions report the unknown status for the first
	// time (previous is empty, so both trigger).
	writeMutagenMonitorTick(t, writer,
		SessionName("proj-a", "repo")+"|"+unknownStatus,
		SessionName("proj-b", "repo")+"|"+unknownStatus,
	)
	// Tick 2: proj-a transitions away, proj-b stays (no re-trigger).
	writeMutagenMonitorTick(t, writer,
		SessionName("proj-a", "repo")+"|Watching",
		SessionName("proj-b", "repo")+"|"+unknownStatus,
	)
	// Tick 3: proj-a transitions back into the unknown status — a
	// third distinct occurrence of the same status value.
	writeMutagenMonitorTick(t, writer,
		SessionName("proj-a", "repo")+"|"+unknownStatus,
		SessionName("proj-b", "repo")+"|"+unknownStatus,
	)

	require.Eventually(t, func() bool {
		rowA, _ := cache.ProjectRow("proj-a")
		return rowA.MutagenHealth.Status == MutagenOK
	}, 2*time.Second, 10*time.Millisecond)

	require.NoError(t, writer.Close())
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("subscribeMutagenMonitorFromReader did not return after writer close")
	}

	require.NoError(t, w.Close())
	var buf bytes.Buffer
	_, err = io.Copy(&buf, r)
	require.NoError(t, err)

	assert.Equal(t, 1, strings.Count(buf.String(), unknownStatus),
		"expected exactly one log line for the unrecognized status; got: %s", buf.String())
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

// TestSubscribeMutagenMonitorFromReader_ObservedTickSignal pins the
// return-value contract that the outer loop's backoff-reset gate
// depends on: a reader that emits at least one complete tick (session
// line + marker) returns true; a reader that closes before the first
// marker returns false. Without this signal, the outer loop's
// "successful reconnect resets backoff" logic would reset even when
// the subprocess died before ever serving state — a start-then-die
// crash loop would stay at the initial 5s cadence forever, defeating
// the exponential backoff.
func TestSubscribeMutagenMonitorFromReader_ObservedTickSignal(t *testing.T) {
	t.Run("full tick emitted", func(t *testing.T) {
		cache := NewStateCache()
		cache.SetMacCwd("proj-a", "/a")
		body := SessionName("proj-a", "repo") + "|Watching\n" + mutagenMonitorTickMarker + "\n"
		got := subscribeMutagenMonitorFromReader(context.Background(), strings.NewReader(body), cache)
		assert.True(t, got, "reader with a complete tick must return true")
	})

	t.Run("no tick marker before EOF", func(t *testing.T) {
		cache := NewStateCache()
		got := subscribeMutagenMonitorFromReader(context.Background(), strings.NewReader(""), cache)
		assert.False(t, got, "empty reader must return false — no tick observed")
	})

	t.Run("session lines but no marker", func(t *testing.T) {
		cache := NewStateCache()
		cache.SetMacCwd("proj-a", "/a")
		// Session data arrived but the subprocess died before its
		// first tick boundary — from the outer loop's perspective this
		// is still a crash-before-first-full-observation.
		body := SessionName("proj-a", "repo") + "|Watching\n"
		got := subscribeMutagenMonitorFromReader(context.Background(), strings.NewReader(body), cache)
		assert.False(t, got, "session lines without a tick marker must return false")
	})
}

// ---------- subscribeMutagenMonitor (outer loop) ----------

// TestSubscribeMutagenMonitor_MarksDeadOnMonitorExit proves the fix for
// the real gap this monitor had: when the `mutagen sync monitor`
// subprocess exits — here, a fake mutagen binary that exits
// immediately with no output, standing in for the Mac-side mutagen
// daemon dying underneath it — nothing else writes to
// cache.MutagenHealth until the outer loop reconnects and observes a
// fresh tick. Without marking known projects dead on exit, a
// previously-healthy project would keep reporting "ok" for up to the
// watchdog's 60s tick, defeating the near-real-time signal. Seeds the
// cache with two known-healthy projects so there is something for the
// exit to flip.
func TestSubscribeMutagenMonitor_MarksDeadOnMonitorExit(t *testing.T) {
	cache := NewStateCache()
	cache.SetMacCwd("proj-a", "/a")
	cache.SetMacCwd("proj-b", "/b")
	// markAllProjectsMutagenDead skips non-running projects (a stopped
	// project has no sync sessions to report dead about); mark both
	// running so the loop actually flips them.
	cache.SetVMState("proj-a", VMRunning)
	cache.SetVMState("proj-b", VMRunning)
	cache.SetMutagenHealth("proj-a", MutagenHealth{Status: MutagenOK})
	cache.SetMutagenHealth("proj-b", MutagenHealth{Status: MutagenOK})

	fakeMutagen := filepath.Join(t.TempDir(), "mutagen")
	require.NoError(t, os.WriteFile(fakeMutagen, []byte("#!/bin/sh\nexit 0\n"), 0o755))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		subscribeMutagenMonitor(ctx, fakeMutagen, identity.Config{Name: "devm-test"}, cache)
		close(done)
	}()

	require.Eventually(t, func() bool {
		rowA, _ := cache.ProjectRow("proj-a")
		rowB, _ := cache.ProjectRow("proj-b")
		return rowA.MutagenHealth.Status == MutagenDead && rowB.MutagenHealth.Status == MutagenDead
	}, 2*time.Second, 10*time.Millisecond, "expected both known projects marked dead once the monitor subprocess exited")

	// Interrupt the reconnect backoff so the loop exits promptly
	// instead of respawning the fake binary for up to
	// mutagenMonitorReconnectBackoffInitial.
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("subscribeMutagenMonitor did not return after ctx cancellation")
	}
}

// TestSubscribeMutagenMonitor_PropagatesShimEnvToSubprocess pins the
// design assumption that motivated commit 49987f6: if the mutagen
// daemon dies mid-flight, the monitor's reconnect can trigger mutagen's
// own auto-daemon-spawn, which inherits the calling process's env. The
// spawned daemon MUST see MUTAGEN_SSH_PATH — without it, its SSH
// transport falls through to the system ssh client and every later
// `sync create` fails on hostname resolution.
//
// This test uses a fake `mutagen` shim that captures its own env to a
// file rather than executing the real binary. The assertion is on the
// captured env: it contains MUTAGEN_SSH_PATH set to the identity's
// mutagen-ssh-dir. If a future refactor drops the shim env from the
// monitor's exec.Command, this fails.
func TestSubscribeMutagenMonitor_PropagatesShimEnvToSubprocess(t *testing.T) {
	tmp := t.TempDir()
	envDump := filepath.Join(tmp, "env-dump")
	fakeMutagen := filepath.Join(tmp, "mutagen")
	// Shell script that writes MUTAGEN_SSH_PATH (via a "key=val" line
	// that's easy to grep) then exits. The exit causes
	// subscribeMutagenMonitor's outer loop to iterate; the ctx cancel
	// below stops the loop before the reconnect backoff elapses.
	// `printf` is used explicitly rather than relying on `env` being
	// found via $PATH — a test-provided fake $PATH could omit it.
	require.NoError(t, os.WriteFile(
		fakeMutagen,
		[]byte(`#!/bin/sh
printf 'MUTAGEN_SSH_PATH=%s\n' "$MUTAGEN_SSH_PATH" > `+envDump+`
exit 0
`),
		0o755,
	))

	// Use a Config whose Name is deterministic so MutagenSSHDir(cfg) is
	// a predictable string to assert against.
	cfg := identity.Config{Name: "devm-test-shim-env"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		subscribeMutagenMonitor(ctx, fakeMutagen, cfg, NewStateCache())
		close(done)
	}()

	require.Eventually(t, func() bool {
		_, err := os.Stat(envDump)
		return err == nil
	}, 2*time.Second, 10*time.Millisecond, "fake mutagen shim never ran or never dumped env")

	body, err := os.ReadFile(envDump)
	require.NoError(t, err)
	want := "MUTAGEN_SSH_PATH=" + MutagenSSHDir(cfg)
	assert.Contains(t, string(body), want,
		"monitor subprocess env must carry the shim path — otherwise mutagen's auto-daemon-spawn inherits a bare env and its SSH transport falls through to system ssh")

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("subscribeMutagenMonitor did not return after ctx cancellation")
	}
}

// TestMarkAllProjectsMutagenDead_SkipsStoppedProjects pins that a
// stopped/absent project is NOT flipped to MutagenDead when the
// monitor subprocess exits: a stopped project has no sync sessions to
// report dead about, and `devm stop myapp` should not make `myapp`
// show mutagen_health: dead the next time some OTHER project's
// monitor hiccups.
func TestMarkAllProjectsMutagenDead_SkipsStoppedProjects(t *testing.T) {
	cache := NewStateCache()

	cache.SetMacCwd("running-proj", "/r")
	cache.SetVMState("running-proj", VMRunning)
	cache.SetMutagenHealth("running-proj", MutagenHealth{Status: MutagenOK})

	cache.SetMacCwd("stopped-proj", "/s")
	cache.SetVMState("stopped-proj", VMStopped)
	cache.SetMutagenHealth("stopped-proj", MutagenHealth{Status: MutagenOK})

	cache.SetMacCwd("absent-proj", "/a")
	cache.SetVMState("absent-proj", VMAbsent)
	cache.SetMutagenHealth("absent-proj", MutagenHealth{Status: MutagenOK})

	markAllProjectsMutagenDead(cache)

	running, _ := cache.ProjectRow("running-proj")
	assert.Equal(t, MutagenDead, running.MutagenHealth.Status,
		"running project must flip to dead when the monitor exits")

	stopped, _ := cache.ProjectRow("stopped-proj")
	assert.Equal(t, MutagenOK, stopped.MutagenHealth.Status,
		"stopped project must NOT flip to dead — no sessions to report dead about")

	absent, _ := cache.ProjectRow("absent-proj")
	assert.Equal(t, MutagenOK, absent.MutagenHealth.Status,
		"absent project must NOT flip to dead — no sessions to report dead about")
}
