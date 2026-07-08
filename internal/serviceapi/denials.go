package serviceapi

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"
	"sync"
	"time"
)

// Denials is the per-project count of hostnames iron-proxy has rejected
// due to allow-list mismatches. It exists so `devm denials` can answer
// "what would I need to allow to make this work" without the user having
// to grep proxy logs by hand.
//
// The map lives only in daemon memory — resets on daemon restart and on
// iron-proxy respawn (see SpawnIronProxy). Transient by design: allow-lists
// change often during iteration, and stale counts from a prior config are
// worse than no counts.
type Denials struct {
	mu        sync.Mutex
	byProject map[string]map[string]*denialCounter
}

type denialCounter struct {
	count     int
	firstSeen time.Time
	lastSeen  time.Time
}

// Denial is one host's roll-up, safe to serialise as JSON. Times are
// UTC-normalised for stable output across clients.
type Denial struct {
	Host      string    `json:"host"`
	Count     int       `json:"count"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// NewDenials returns an empty tracker.
func NewDenials() *Denials {
	return &Denials{byProject: map[string]map[string]*denialCounter{}}
}

// Record bumps the count for host under projectID, updating lastSeen.
// First observation sets firstSeen. Called from the supervisor tap on
// every parsed reject audit line.
func (d *Denials) Record(projectID, host string, when time.Time) {
	if projectID == "" || host == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	proj, ok := d.byProject[projectID]
	if !ok {
		proj = map[string]*denialCounter{}
		d.byProject[projectID] = proj
	}
	c, ok := proj[host]
	if !ok {
		proj[host] = &denialCounter{count: 1, firstSeen: when, lastSeen: when}
		return
	}
	c.count++
	c.lastSeen = when
}

// Reset drops all counts for projectID. Called on iron-proxy respawn so
// counts reflect the currently running config, not a stale prior one.
func (d *Denials) Reset(projectID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.byProject, projectID)
}

// Snapshot returns a copy of the current counts for projectID, sorted by
// count descending (most-denied first). Empty slice when the project has
// no denials.
func (d *Denials) Snapshot(projectID string) []Denial {
	d.mu.Lock()
	defer d.mu.Unlock()
	proj := d.byProject[projectID]
	out := make([]Denial, 0, len(proj))
	for h, c := range proj {
		out = append(out, Denial{
			Host:      h,
			Count:     c.count,
			FirstSeen: c.firstSeen,
			LastSeen:  c.lastSeen,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Host < out[j].Host
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
	t.dst.Record(t.projectID, rec.Audit.Host, when.UTC())
}
