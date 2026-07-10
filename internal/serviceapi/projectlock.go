package serviceapi

import "sync"

// ProjectLocks provides per-project mutual exclusion for the daemon's
// state-mutating endpoints. Replaces the CLI-side .devm/lock flock:
// serialization moves inside the daemon so every state mutation
// (start, stop, teardown, reconcile, apply-egress) queues against
// concurrent invocations for the same project.
//
// Daemon restart drops the map — same semantics as flock dying with
// its CLI process today.
type ProjectLocks struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewProjectLocks returns an empty ProjectLocks.
func NewProjectLocks() *ProjectLocks {
	return &ProjectLocks{locks: make(map[string]*sync.Mutex)}
}

// Lock blocks until the calling goroutine owns the mutex for
// projectID. Returns an unlock closure — use with defer:
//
//	unlock := p.Lock(id)
//	defer unlock()
func (p *ProjectLocks) Lock(projectID string) func() {
	p.mu.Lock()
	m, ok := p.locks[projectID]
	if !ok {
		m = &sync.Mutex{}
		p.locks[projectID] = m
	}
	p.mu.Unlock()
	m.Lock()
	return m.Unlock
}
