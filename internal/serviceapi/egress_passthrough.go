package serviceapi

import (
	"sync"
	"time"
)

// defaultPassthroughSeconds is how long `devm passthrough` opens
// egress for when the caller doesn't specify `--for`. Deliberately
// short: every second of passthrough is a second where any host on
// any port is reachable without MITM or audit log.
const defaultPassthroughSeconds = 30

// egressPassthroughEntry tracks one project's active passthrough
// window: when it auto-restores and the timer that will fire the
// restore. Empty (zero-valued expiresAt, nil restore) is a valid
// "no window active" state, but the store's get() returns ok=false
// for that case — an entry only exists in the map while a window is
// open or its bookkeeping is mid-flight (setTimer between put and
// timer fire).
type egressPassthroughEntry struct {
	expiresAt time.Time
	restore   *time.Timer
}

// egressPassthroughStore tracks each project's egress-passthrough
// window state: a mutex-guarded map plus a per-project relock-style
// timer.
type egressPassthroughStore struct {
	mu sync.Mutex
	m  map[string]egressPassthroughEntry
}

func newEgressPassthroughStore() *egressPassthroughStore {
	return &egressPassthroughStore{m: make(map[string]egressPassthroughEntry)}
}

func (s *egressPassthroughStore) put(projectID string, expiresAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.m[projectID]
	e.expiresAt = expiresAt
	s.m[projectID] = e
}

func (s *egressPassthroughStore) get(projectID string) (egressPassthroughEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[projectID]
	return e, ok
}

// setTimer installs t as the project's pending restore timer,
// stopping and replacing whatever timer was previously scheduled so
// timers never leak or double-fire.
func (s *egressPassthroughStore) setTimer(projectID string, t *time.Timer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.m[projectID]
	if e.restore != nil {
		e.restore.Stop()
	}
	e.restore = t
	s.m[projectID] = e
}

func (s *egressPassthroughStore) stopTimer(projectID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[projectID]
	if !ok || e.restore == nil {
		return
	}
	e.restore.Stop()
	e.restore = nil
	s.m[projectID] = e
}

func (s *egressPassthroughStore) del(projectID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.m[projectID]; ok && e.restore != nil {
		e.restore.Stop()
	}
	delete(s.m, projectID)
}

// egressPassthroughState is the daemon-wide singleton. RegisterVMHandlers
// reads and mutates it; unit tests reset it via
// t.Cleanup(func() { … del(name) }).
var egressPassthroughState = newEgressPassthroughStore()
