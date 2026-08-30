package serviceapi

import (
	"bytes"
	"encoding/json"
	"io"
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
// iron-proxy respawn (see SpawnIronProxy). Transient by design: allow-lists
// change often during iteration, and stale counts from a prior config are
// worse than no counts.
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
// firstSeen. Called from the supervisor tap on every parsed reject
// audit line.
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

// Reset drops all counts for projectID. Called on iron-proxy respawn so
// counts reflect the currently running config, not a stale prior one.
func (d *Denials) Reset(projectID string) {
	d.clearProject(projectID)
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

// TapWriter returns an io.Writer that consumes iron-proxy's structured
// audit log and records reject events into d for projectID. The writer
// is line-buffered internally — safe to attach as one side of an
// io.MultiWriter alongside the on-disk log file.
//
// Non-JSON lines, allow / stub / error actions, and lines without an
// audit.host field are silently ignored: iron-proxy's log stream is a
// mix of audit records, startup / shutdown noise, and eventual error
// spam. The tap treats anything it can't classify as a reject as noise.
func (d *Denials) TapWriter(projectID string) io.Writer {
	return &denialsTap{projectID: projectID, dst: d}
}

type denialsTap struct {
	mu        sync.Mutex
	projectID string
	dst       *Denials
	partial   []byte
}

func (t *denialsTap) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.partial = append(t.partial, p...)
	for {
		i := bytes.IndexByte(t.partial, '\n')
		if i < 0 {
			break
		}
		line := t.partial[:i]
		t.partial = t.partial[i+1:]
		t.consume(line)
	}
	return len(p), nil
}

// consume parses one iron-proxy audit line. Bails on the fast path when
// the line clearly isn't a reject — most log volume is allow, and
// json.Unmarshal per line is wasted work.
func (t *denialsTap) consume(line []byte) {
	if !bytes.Contains(line, []byte(`"msg":"request"`)) {
		return
	}
	if !bytes.Contains(line, []byte(`"action":"reject"`)) {
		return
	}
	var rec struct {
		Time  time.Time `json:"time"`
		Audit struct {
			Host   string `json:"host"`
			Path   string `json:"path"`
			Method string `json:"method"`
			Action string `json:"action"`
		} `json:"audit"`
	}
	if err := json.Unmarshal(line, &rec); err != nil {
		return
	}
	if rec.Audit.Action != "reject" || rec.Audit.Host == "" {
		return
	}
	when := rec.Time
	if when.IsZero() {
		when = time.Now()
	}
	t.dst.Record(t.projectID, rec.Audit.Host, rec.Audit.Path, rec.Audit.Method, when.UTC())
}
