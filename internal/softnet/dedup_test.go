package softnet

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// TestDedupLoggerFirstEmits pins that the first hit for a fresh key
// logs immediately with no suppressed tail.
func TestDedupLoggerFirstEmits(t *testing.T) {
	var buf bytes.Buffer
	old := logOut
	logOut = &buf
	defer func() { logOut = old }()

	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d := newDedupLogger(time.Minute)
	d.now = func() time.Time { return t0 }

	d.Logf("k", "egress reject %s", "1.2.3.4:22")

	got := buf.String()
	if !strings.Contains(got, "[softnet] egress reject 1.2.3.4:22") {
		t.Fatalf("missing first-emit line: %q", got)
	}
	if strings.Contains(got, "repeated") {
		t.Fatalf("first emit must not carry suppressed tail: %q", got)
	}
}

// TestDedupLoggerSuppressesWithinWindow pins that repeat hits inside
// the window write nothing.
func TestDedupLoggerSuppressesWithinWindow(t *testing.T) {
	var buf bytes.Buffer
	old := logOut
	logOut = &buf
	defer func() { logOut = old }()

	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d := newDedupLogger(time.Minute)
	d.now = func() time.Time { return t0 }

	d.Logf("k", "reject")
	beforeSecond := buf.Len()
	// still at t0, still within window
	d.Logf("k", "reject")
	d.Logf("k", "reject")
	if buf.Len() != beforeSecond {
		t.Fatalf("repeats inside window must not log; buf grew: %q", buf.String()[beforeSecond:])
	}
}

// TestDedupLoggerEmitsWithSuppressedCountPastWindow pins that the
// first hit past the window logs a "(repeated Nx in last W)" tail
// counting the silenced events.
func TestDedupLoggerEmitsWithSuppressedCountPastWindow(t *testing.T) {
	var buf bytes.Buffer
	old := logOut
	logOut = &buf
	defer func() { logOut = old }()

	current := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d := newDedupLogger(time.Minute)
	d.now = func() time.Time { return current }

	d.Logf("k", "reject") // emits (first)
	// 5 suppressed hits at the same instant
	for i := 0; i < 5; i++ {
		d.Logf("k", "reject")
	}
	// jump past the window
	current = current.Add(2 * time.Minute)
	buf.Reset()
	d.Logf("k", "reject")

	got := buf.String()
	if !strings.Contains(got, "[softnet] reject") {
		t.Fatalf("expected 'reject' in %q", got)
	}
	if !strings.Contains(got, "repeated 5x in last 1m0s") {
		t.Fatalf("expected suppressed tail 'repeated 5x in last 1m0s' in %q", got)
	}
}

// TestDedupLoggerRearmsAfterEmit pins that after a window-crossing
// emit, the suppressed counter resets to 0 for the next cycle.
func TestDedupLoggerRearmsAfterEmit(t *testing.T) {
	var buf bytes.Buffer
	old := logOut
	logOut = &buf
	defer func() { logOut = old }()

	current := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d := newDedupLogger(time.Minute)
	d.now = func() time.Time { return current }

	d.Logf("k", "reject")
	d.Logf("k", "reject") // suppressed
	current = current.Add(2 * time.Minute)
	d.Logf("k", "reject") // emits with suppressed=1

	// Immediately after the window-crossing emit, next hits are
	// suppressed again and the counter starts from zero.
	buf.Reset()
	d.Logf("k", "reject")
	if buf.Len() != 0 {
		t.Fatalf("post-rearm hit should be suppressed, logged: %q", buf.String())
	}
	current = current.Add(2 * time.Minute)
	buf.Reset()
	d.Logf("k", "reject")
	got := buf.String()
	if !strings.Contains(got, "repeated 1x") {
		t.Fatalf("expected fresh suppressed count starting at 1x, got %q", got)
	}
}

// TestDedupLoggerKeysAreIndependent pins that different keys don't
// share dedup state — a burst on key A never silences key B.
func TestDedupLoggerKeysAreIndependent(t *testing.T) {
	var buf bytes.Buffer
	old := logOut
	logOut = &buf
	defer func() { logOut = old }()

	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d := newDedupLogger(time.Minute)
	d.now = func() time.Time { return t0 }

	d.Logf("a", "reject a")
	d.Logf("b", "reject b")

	got := buf.String()
	if !strings.Contains(got, "reject a") {
		t.Fatalf("key a did not log: %q", got)
	}
	if !strings.Contains(got, "reject b") {
		t.Fatalf("key b did not log: %q", got)
	}
}
