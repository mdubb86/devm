package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Rediscovery of orphaned runs after a `systemctl restart
// gdevm-serve.service`. The unit is KillMode=process so bash
// children survive the restart; without rediscovery those children
// would be invisible to the new gdevm serve — not listed by
// `gdevm runs`, not stoppable by `gdevm stop <name>`. Rediscovery
// scans per-run state files written at run start and re-registers
// the still-live ones as orphans in the registry.
//
// Orphans differ from first-party runs in one way: we are not their
// parent, so we cannot wait() for an exit code. Their `cmd` field
// is nil (the sentinel); death is detected by polling
// Kill(pgid, 0) and `exit_code` is recorded as -1 ("unknown").

// runStateVersion tags the on-disk schema. A mismatch on rediscovery
// causes the file to be removed — orphans would be invisible anyway
// since the state fields can't be trusted.
const runStateVersion = 1

// orphanPollInterval is how often the orphan reaper tests whether a
// rediscovered process group is still alive. Package var so tests
// can shrink it.
var orphanPollInterval = 2 * time.Second

// runState is the on-disk shape persisted next to a run's log file so
// gdevm serve can rediscover an orphaned run after a daemon restart.
// Attached runs' state is written too: on restart the attached client
// is gone (EOF on the TCP pipe) but the bash child survives under
// KillMode=process, so from the new daemon's point of view it is an
// orphan just like a detached run.
type runState struct {
	Version   int       `json:"version"`
	Name      string    `json:"name"`
	Pgid      int       `json:"pgid"`
	StartedAt time.Time `json:"started_at"`
}

// stateFileForName is the on-disk path of a run's persisted state.
// Lives next to the log file so the two travel together on backup /
// cleanup.
func stateFileForName(name string) string {
	return filepath.Join(logsDir, name+".state")
}

// writeRunState atomically writes r's rediscovery state to
// <logsDir>/<name>.state. Called after cmd.Start, so pgid is known.
func writeRunState(r *run) error {
	s := runState{
		Version:   runStateVersion,
		Name:      r.name,
		Pgid:      r.pgid,
		StartedAt: r.startedAt,
	}
	body, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("marshal run state: %w", err)
	}
	final := stateFileForName(r.name)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return fmt.Errorf("write run state: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename run state: %w", err)
	}
	return nil
}

// removeRunState deletes a run's persisted state file. Called from
// the reaper goroutine after bash exits and from the orphan poller
// when it observes the pgroup gone, so stale state files don't
// accumulate. Idempotent.
func removeRunState(name string) {
	_ = os.Remove(stateFileForName(name))
}

// rediscoverRuns scans logsDir for *.state files left by a previous
// gdevm serve. Each file that references a still-live process group
// becomes an orphan in reg; each file whose pgroup is gone, or whose
// contents are unparseable, is removed. Called once at serve startup
// before the HTTP listener accepts traffic.
//
// Errors are logged but never fatal — a single broken state file
// must not prevent serve from starting, since the operator's recovery
// path is `gdevm stop <name>` on surviving runs.
func rediscoverRuns(reg *runRegistry) {
	entries, err := os.ReadDir(logsDir)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("rediscover: read %s: %v", logsDir, err)
		}
		return
	}
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		if filepath.Ext(name) != ".state" {
			continue
		}
		path := filepath.Join(logsDir, name)
		body, err := os.ReadFile(path)
		if err != nil {
			log.Printf("rediscover: read %s: %v", path, err)
			continue
		}
		var s runState
		if err := json.Unmarshal(body, &s); err != nil {
			log.Printf("rediscover: parse %s: %v (removing)", path, err)
			_ = os.Remove(path)
			continue
		}
		if s.Version != runStateVersion {
			log.Printf("rediscover: %s is version %d, want %d (removing)", path, s.Version, runStateVersion)
			_ = os.Remove(path)
			continue
		}
		if s.Name == "" || s.Pgid <= 0 {
			log.Printf("rediscover: %s missing fields (removing)", path)
			_ = os.Remove(path)
			continue
		}
		// Kill(-pgid, 0) sends no signal but returns ESRCH when the
		// process group has no members. The negative pgid dispatches
		// to every member of the group, so this survives the leader
		// exiting while a child it backgrounded still holds the
		// group (positive-pgid Kill would miss that case, since it
		// only checks the specific leader pid). EPERM would mean the
		// group exists but we can't signal it — shouldn't happen
		// when both are root.
		if err := syscall.Kill(-s.Pgid, 0); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				log.Printf("rediscover: pgroup %d for %q is empty, cleaning state", s.Pgid, s.Name)
			} else {
				log.Printf("rediscover: pgroup %d for %q: %v, cleaning state", s.Pgid, s.Name, err)
			}
			_ = os.Remove(path)
			continue
		}
		r := &run{
			name: s.Name,
			// cmd: nil is the orphan sentinel. Readers that would
			// reach for cmd.Process must branch on it being nil.
			cmd:       nil,
			pgid:      s.Pgid,
			startedAt: s.StartedAt,
			// Attached is impossible after a daemon restart — the
			// TCP pipe is gone — so a rediscovered run is always
			// detached from the new daemon's perspective.
			detach:   true,
			logPath:  logFileForName(s.Name),
			exitDone: make(chan struct{}),
		}
		if _, ok := reg.startExclusive(s.Name, r); !ok {
			// Startup is single-threaded; collision would mean two
			// state files for one name, which shouldn't happen.
			log.Printf("rediscover: collision registering %q, skipping", s.Name)
			continue
		}
		log.Printf("rediscover: registered %q (pgid %d)", s.Name, s.Pgid)
		go pollOrphanExit(r)
	}
}

// pollOrphanExit watches a rediscovered run's process group. We can't
// wait() for it (not our child), so we poll Kill(-pgid, 0) and treat
// ESRCH as the group being empty. Exit code is -1 ("unknown"),
// reflected in `gdevm runs` as `exit ?`.
func pollOrphanExit(r *run) {
	ticker := time.NewTicker(orphanPollInterval)
	defer ticker.Stop()
	for range ticker.C {
		err := syscall.Kill(-r.pgid, 0)
		if err == nil {
			continue
		}
		if errors.Is(err, syscall.ESRCH) {
			r.exitCode = -1
			close(r.exitDone)
			removeRunState(r.name)
			return
		}
		// EPERM would mean the pgroup still exists but we can't
		// signal it; keep polling.
	}
}

// isOrphan reports whether r was rediscovered (not spawned by this
// gdevm serve). The sentinel is cmd == nil.
func isOrphan(r *run) bool { return r.cmd == nil }

// leaderPid returns the pid of the pgroup leader for r, suitable for
// `gdevm runs` display. For first-party runs it is the bash pid; for
// orphans it is the saved pgid (which is the leader's pid by
// construction, since we called setpgid on cmd.Start).
func leaderPid(r *run) int {
	if isOrphan(r) {
		return r.pgid
	}
	return r.cmd.Process.Pid
}
