package serviceapi

import (
	"sort"
	"sync"
	"time"
)

// Denials is the per-project count of hostnames the policy authority has
// rejected due to allow-list mismatches. Recorded by PolicyAuthority at
// the reject decision (see PolicyAuthority.SetAllowlist,
// policyService.TransformRequest) so `devm denials` can answer "what
// would I need to allow to make this work" without the user having to
// grep proxy logs by hand.
//
// The map lives only in daemon memory — resets on daemon restart and on
// allow-list replacement (see PolicyAuthority.SetAllowlist). Transient by
// design: allow-lists change often during iteration, and stale counts
// from a prior config are worse than no counts.
type Denials struct {
	mu        sync.Mutex
	byProject map[string]map[denialKey]*denialCounter
}

// denialKey rolls up by (host, path). Path-scoped allow rules
// (v0.18.0+) mean a reject at github.com/anthropics/... and one at
// github.com/other/... are two different allowlist decisions; the
// user needs each URL separately to know what to add.
type denialKey struct {
	host string
	path string
}

type denialCounter struct {
	count     int
	method    string // last-seen HTTP method for this (host, path)
	firstSeen time.Time
	lastSeen  time.Time
}

// Denial is one (host, path) roll-up, safe to serialise as JSON. Times
// are UTC-normalised for stable output across clients. Method is the
// most-recent-seen HTTP method for this URL — informational, not part
// of the rollup key.
type Denial struct {
	Host      string    `json:"host"`
	Path      string    `json:"path"`
	Method    string    `json:"method"`
	Count     int       `json:"count"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// NewDenials returns an empty tracker.
func NewDenials() *Denials {
	return &Denials{byProject: map[string]map[denialKey]*denialCounter{}}
}

// Record bumps the count for (host, path) under projectID, updating
// lastSeen and refreshing the recorded method. First observation sets
// firstSeen. Called by PolicyAuthority at the reject decision.
func (d *Denials) Record(projectID, host, path, method string, when time.Time) {
	if projectID == "" || host == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	proj, ok := d.byProject[projectID]
	if !ok {
		proj = map[denialKey]*denialCounter{}
		d.byProject[projectID] = proj
	}
	k := denialKey{host: host, path: path}
	c, ok := proj[k]
	if !ok {
		proj[k] = &denialCounter{count: 1, method: method, firstSeen: when, lastSeen: when}
		return
	}
	c.count++
	c.method = method
	c.lastSeen = when
}

// clearProject drops all counts for projectID.
func (d *Denials) clearProject(projectID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.byProject, projectID)
}

// invalidateResolved deletes every (host, path) row under projectID for
// which allowed reports true — i.e. rows a new allowlist would now let
// through. It walks only its own rows; matching semantics are entirely
// the caller's (PolicyAuthority.SetAllowlist passes a closure over
// policymatch.Allowed) so this package never needs to know allowlist
// syntax.
func (d *Denials) invalidateResolved(projectID string, allowed func(host, path string) bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	proj, ok := d.byProject[projectID]
	if !ok {
		return
	}
	for k := range proj {
		if allowed(k.host, k.path) {
			delete(proj, k)
		}
	}
}

// Snapshot returns a copy of the current counts for projectID, sorted by
// count descending (most-denied first). Empty slice when the project has
// no denials.
func (d *Denials) Snapshot(projectID string) []Denial {
	d.mu.Lock()
	defer d.mu.Unlock()
	proj := d.byProject[projectID]
	out := make([]Denial, 0, len(proj))
	for k, c := range proj {
		out = append(out, Denial{
			Host:      k.host,
			Path:      k.path,
			Method:    c.method,
			Count:     c.count,
			FirstSeen: c.firstSeen,
			LastSeen:  c.lastSeen,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		if out[i].Host != out[j].Host {
			return out[i].Host < out[j].Host
		}
		return out[i].Path < out[j].Path
	})
	return out
}
