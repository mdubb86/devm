package serviceapi

import (
	"sync"
	"time"
)

type VMState string

const (
	VMAbsent  VMState = "absent"
	VMStopped VMState = "stopped"
	VMRunning VMState = "running"
)

type MutagenStatus string

const (
	MutagenOK   MutagenStatus = "ok"
	MutagenDead MutagenStatus = "dead"
)

// MutagenHealth summarises the daemon-wide mutagen process state as
// reflected in the cache. The cache tracks daemon-wide status but
// exposes it per project so consumers can attribute mutagen outages
// to specific projects when needed.
type MutagenHealth struct {
	Status MutagenStatus
}

// ApproveStateSummary is the cache-side subset of approve-gate signals
// safe to hold in memory. Byte payloads (current_devm_bytes,
// approved_devm_bytes) are NOT cached — those live on disk and are
// read at /vm/approve-state request time. This summary supports quick
// "is it diverged?" polling via /status/all and future consumers.
type ApproveStateSummary struct {
	Diverged        bool
	CurrentDevmSHA  string
	ApprovedDevmSHA string
	CurrentMeSHA    string
	ApprovedMeSHA   string
	ApprovedSince   *time.Time
}

// PopSessionSummary is what /pop-session-summary returns. Kept
// identical to the JSON field shape so migration is a straight
// copy-out.
type PopSessionSummary struct {
	Count            int
	OldestAgeSeconds int64
}

type ProjectRow struct {
	MacCwd           string
	VMState          VMState
	IronProxyHealth  ProxyHealth
	MutagenHealth    MutagenHealth
	ApproveState     ApproveStateSummary
	PopSessions      PopSessionSummary
	LastReconciledAt time.Time
}

type GlobalState struct {
	Build            Build
	MutagenDaemonPID int
	ProxyReady       bool
	LastReconciledAt time.Time
}

// StateCache is the daemon's single authoritative model of "what is
// the current state of every project?" Reads are O(1) map lookups
// under RLock. Writes take the exclusive Lock but do no I/O — callers
// must produce final values off-lock and then call one setter each.
type StateCache struct {
	mu     sync.RWMutex
	rows   map[string]ProjectRow
	global GlobalState
}

func NewStateCache() *StateCache {
	return &StateCache{rows: make(map[string]ProjectRow)}
}

func (c *StateCache) Global() GlobalState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.global
}

func (c *StateCache) ProjectRow(name string) (ProjectRow, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	row, ok := c.rows[name]
	return row, ok
}

func (c *StateCache) AllProjectRows() map[string]ProjectRow {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]ProjectRow, len(c.rows))
	for k, v := range c.rows {
		out[k] = v
	}
	return out
}

func (c *StateCache) SetBuild(b Build) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.global.Build = b
}

func (c *StateCache) SetMutagenDaemonPID(pid int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.global.MutagenDaemonPID = pid
}

func (c *StateCache) SetProxyReady(ready bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.global.ProxyReady = ready
}

func (c *StateCache) TouchGlobalReconciled() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.global.LastReconciledAt = time.Now()
}

func (c *StateCache) SetMacCwd(name, cwd string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	row := c.rows[name]
	row.MacCwd = cwd
	c.rows[name] = row
}

func (c *StateCache) SetVMState(name string, state VMState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	row := c.rows[name]
	row.VMState = state
	c.rows[name] = row
}

func (c *StateCache) SetIronProxyHealth(name string, h ProxyHealth) {
	c.mu.Lock()
	defer c.mu.Unlock()
	row := c.rows[name]
	row.IronProxyHealth = h
	c.rows[name] = row
}

func (c *StateCache) SetMutagenHealth(name string, h MutagenHealth) {
	c.mu.Lock()
	defer c.mu.Unlock()
	row := c.rows[name]
	row.MutagenHealth = h
	c.rows[name] = row
}

func (c *StateCache) SetApproveState(name string, s ApproveStateSummary) {
	c.mu.Lock()
	defer c.mu.Unlock()
	row := c.rows[name]
	row.ApproveState = s
	c.rows[name] = row
}

func (c *StateCache) SetPopSessionSummary(name string, s PopSessionSummary) {
	c.mu.Lock()
	defer c.mu.Unlock()
	row := c.rows[name]
	row.PopSessions = s
	c.rows[name] = row
}

func (c *StateCache) TouchProjectReconciled(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	row := c.rows[name]
	row.LastReconciledAt = time.Now()
	c.rows[name] = row
}

func (c *StateCache) RemoveProject(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.rows, name)
}
