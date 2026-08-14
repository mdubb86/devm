package softnet

import (
	"fmt"
	"sync"
	"time"
)

// dedupLogger emits at most one log line per key per window while
// preserving the count of suppressed occurrences between emissions.
// It is the answer to a class of network-path silent-failures where
// the wrong call is either "log every event" (a locked-down guest
// spamming denies drowns real signal) or "log nothing" (which is
// what leaves us blind for hours during incident triage).
//
// First occurrence of a key logs immediately with suppressed=0.
// Subsequent hits within window silently increment the suppressed
// counter. The next hit past window logs "(repeated Nx in last W)"
// and rearms — so a burst that ends silently loses its tail count,
// but any ongoing pattern keeps a heartbeat once per window.
type dedupLogger struct {
	mu     sync.Mutex
	state  map[string]*dedupEntry
	window time.Duration
	now    func() time.Time // injectable for tests
}

type dedupEntry struct {
	lastLoggedAt time.Time
	suppressed   int
}

func newDedupLogger(window time.Duration) *dedupLogger {
	return &dedupLogger{
		state:  make(map[string]*dedupEntry),
		window: window,
		now:    time.Now,
	}
}

// Logf emits format/args prefixed by [softnet], with a
// "(repeated Nx in last W)" tail when suppressed events preceded
// this emission. Callers pass a key that groups semantically-identical
// events — a policy change should re-log, so the key should include
// the policy; a per-target denial should share its key across
// requests to the same target so a hot loop doesn't flood.
func (d *dedupLogger) Logf(key, format string, args ...any) {
	emit, suppressed := d.check(key)
	if !emit {
		return
	}
	msg := fmt.Sprintf(format, args...)
	if suppressed > 0 {
		logf("%s (repeated %dx in last %s)", msg, suppressed, d.window)
	} else {
		logf("%s", msg)
	}
}

// check decides whether this call should emit and returns the count
// of suppressed events since the last emission for this key.
func (d *dedupLogger) check(key string) (emit bool, suppressed int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	s, ok := d.state[key]
	if !ok {
		d.state[key] = &dedupEntry{lastLoggedAt: now}
		return true, 0
	}
	if now.Sub(s.lastLoggedAt) >= d.window {
		n := s.suppressed
		s.lastLoggedAt = now
		s.suppressed = 0
		return true, n
	}
	s.suppressed++
	return false, 0
}
