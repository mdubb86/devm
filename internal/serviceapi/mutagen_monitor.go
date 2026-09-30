package serviceapi

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/mdubb86/devm/internal/daemonlog"
)

// mutagenMonitorTickMarker is a literal line emitted once per redraw
// ("tick") of the monitor template. text/template renders the whole
// template string fresh on every redraw, so a line placed after the
// {{range}} block is guaranteed to appear exactly once per tick,
// following that tick's session lines and preceding the next tick's —
// an unambiguous boundary the subscriber can split on without relying
// on any undocumented mutagen framing (the spike found no reliable
// blank-line separator between ticks; see task-2-report.md finding 5).
const mutagenMonitorTickMarker = "===devm-tick==="

// mutagenMonitorTemplate is the --template argument for `mutagen sync
// monitor`. Verified against the mutagen 0.18.1 binary embedded at
// internal/mutagen/embed/mutagen.gz: `sync monitor --help` confirms
// `--template` is a real flag and `--long-format` is not (only
// `-l`/`--long`, whose spinner mode redraws via bare `\r` with no
// `\n` at all — not bufio.Scanner-compatible; see task-2-report.md
// finding 1-2). `.Name` and `.Status` are real fields on the
// per-session template context (task-2-report.md finding 3), also
// consistent with this package's own internal/mutagen/cli.go, whose
// `SyncList` drives `sync list --template "{{json .}}"` over the same
// underlying session type. Ending every line in a literal "\n" keeps
// every redraw newline-terminated, unlike -l/--long's spinner.
var mutagenMonitorTemplate = "{{range .}}{{.Name}}|{{.Status}}\n{{end}}" + mutagenMonitorTickMarker + "\n"

// mutagenMonitorReconnectBackoff is how long subscribeMutagenMonitor
// waits before retrying after a failed process start, or after the
// monitor process exits (daemon unreachable or mid-restart). Without
// it, a persistently-dead mutagen daemon would make this actor spin
// in a tight respawn loop until the separate mutagen watchdog check
// (up to 60s) repairs it — see watchdog_check_mutagen.go.
var mutagenMonitorReconnectBackoff = 5 * time.Second

// subscribeMutagenMonitor runs `mutagen sync monitor --template
// mutagenMonitorTemplate` and reflects every session's health into
// cache.ProjectRow.MutagenHealth as transitions are observed — within
// the monitor's own redraw cadence (~1-1.5s at rest, faster on a real
// transition per task-2-report.md finding 4), not the watchdog's 60s
// tick. Blocks until ctx is cancelled.
//
// dataDir sets MUTAGEN_DATA_DIRECTORY for the subprocess. This is
// required, not optional: the devm daemon process's own environment
// does not carry it — every other mutagen.CLI invocation in this
// package sets it per-call the same way (see internal/mutagen/cli.go
// CLI.env()) — so without it the subprocess would monitor the wrong
// (default, unrelated) mutagen data directory.
//
// On the monitor process exiting — the mutagen daemon crashed, or was
// never reachable — this loops and reconnects after
// mutagenMonitorReconnectBackoff. It does not itself repair the
// daemon: that's the mutagen watchdog check's job
// (watchdog_check_mutagen.go); this actor only resumes watching once
// the daemon is back.
func subscribeMutagenMonitor(ctx context.Context, mutagenBin, dataDir string, cache *StateCache) {
	env := append(os.Environ(), "MUTAGEN_DATA_DIRECTORY="+dataDir)
	for ctx.Err() == nil {
		cmd := exec.CommandContext(ctx, mutagenBin, "sync", "monitor", "--template", mutagenMonitorTemplate)
		cmd.Env = env
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			daemonlog.Errorf("serviceapi: mutagen monitor: stdout pipe: %v", err)
			markAllProjectsMutagenDead(cache)
			if !sleepOrDone(ctx, mutagenMonitorReconnectBackoff) {
				return
			}
			continue
		}
		if err := cmd.Start(); err != nil {
			daemonlog.Errorf("serviceapi: mutagen monitor: start: %v", err)
			markAllProjectsMutagenDead(cache)
			if !sleepOrDone(ctx, mutagenMonitorReconnectBackoff) {
				return
			}
			continue
		}
		subscribeMutagenMonitorFromReader(ctx, stdout, cache)
		_ = cmd.Wait()
		// The monitor subprocess exited — whether because the mutagen
		// daemon it was watching died or because the reader hit EOF —
		// so nothing is observing session health until the next
		// iteration reconnects. Mark every known project dead now
		// rather than leaving the cache on its last-observed value
		// (typically "ok") until the watchdog's next 60s tick.
		markAllProjectsMutagenDead(cache)
		if ctx.Err() != nil {
			return
		}
		if !sleepOrDone(ctx, mutagenMonitorReconnectBackoff) {
			return
		}
	}
}

// markAllProjectsMutagenDead flips MutagenHealth to dead for every
// project currently known to cache. Called whenever the `mutagen sync
// monitor` subprocess is not running (failed to start, or exited) so
// the cache never reports stale "ok" health during an outage.
func markAllProjectsMutagenDead(cache *StateCache) {
	for name := range cache.AllProjectRows() {
		cache.SetMutagenHealth(name, MutagenHealth{Status: MutagenDead})
	}
}

// sleepOrDone waits for d or ctx cancellation, whichever comes first.
// Returns false iff ctx was cancelled first.
func sleepOrDone(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// subscribeMutagenMonitorFromReader reads mutagenMonitorTemplate
// output from r one tick at a time and, for every session whose
// status changed since the previous tick, resolves the owning project
// and writes its derived MutagenHealth to cache.
//
// `mutagen sync monitor` run with no positional session filter
// re-emits EVERY session's line on every redraw, not just the one
// that changed (task-2-report.md finding 5) — so this cannot treat
// "a new line" as itself a transition event. Instead it accumulates
// each tick's full name->status table (delimited by
// mutagenMonitorTickMarker) and diffs it against the previous tick's
// table, writing only the sessions whose status actually differs —
// including a session seen for the first time, where the previous
// table has no entry for it at all.
//
// Returns cleanly on EOF (r closed) or ctx cancellation — never
// blocks past either.
func subscribeMutagenMonitorFromReader(ctx context.Context, r io.Reader, cache *StateCache) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20) // long session names, many sessions per tick

	previous := map[string]string{}
	tick := map[string]string{}

	for sc.Scan() {
		if ctx.Err() != nil {
			return
		}
		line := sc.Text()
		if line == mutagenMonitorTickMarker {
			applyMutagenMonitorTick(cache, previous, tick)
			previous = tick
			tick = map[string]string{}
			continue
		}
		name, status, ok := parseMutagenMonitorLine(line)
		if !ok {
			continue
		}
		tick[name] = status
	}
	if err := sc.Err(); err != nil {
		daemonlog.Errorf("serviceapi: mutagen monitor: read: %v", err)
	}
}

// applyMutagenMonitorTick writes cache.MutagenHealth for every session
// in tick whose status differs from its value in previous. A session
// name that doesn't resolve to a known project is skipped — it
// belongs to a project this daemon instance doesn't (yet) have a
// cache row for, and will resolve on a later tick once one exists.
func applyMutagenMonitorTick(cache *StateCache, previous, tick map[string]string) {
	for name, status := range tick {
		if previous[name] == status {
			continue
		}
		project, ok := sessionNameToProject(cache, name)
		if !ok {
			continue
		}
		cache.SetMutagenHealth(project, statusToMutagenHealth(status))
	}
}

// parseMutagenMonitorLine splits one "name|status" line rendered by
// mutagenMonitorTemplate. Any line that doesn't match — including a
// stray blank line — is rejected rather than guessed at.
func parseMutagenMonitorLine(line string) (name, status string, ok bool) {
	name, status, ok = strings.Cut(line, "|")
	if !ok || name == "" || status == "" {
		return "", "", false
	}
	return name, status, true
}

// sessionNameToProject resolves a mutagen session name (SessionName's
// "devm-<project>-<label>" convention — mutagen_sessions.go) to the
// devm project it belongs to, by matching against SessionNamePrefix
// for every project currently known to cache. Longest-prefix match,
// so one project name being a literal prefix of another's (e.g.
// "foo" and "foo-bar") still resolves correctly.
func sessionNameToProject(cache *StateCache, sessionName string) (string, bool) {
	bestProject := ""
	bestPrefixLen := -1
	for project := range cache.AllProjectRows() {
		prefix := SessionNamePrefix(project)
		if strings.HasPrefix(sessionName, prefix) && len(prefix) > bestPrefixLen {
			bestProject = project
			bestPrefixLen = len(prefix)
		}
	}
	if bestPrefixLen < 0 {
		return "", false
	}
	return bestProject, true
}

// mutagenFailedStatuses are the .Status values that mean a session's
// sync loop has stopped making progress — a genuine failure, not a
// normal phase of the sync cycle. Sourced from the mutagen 0.18.1
// embedded binary's synchronization.Status protobuf enum, extracted
// directly out of internal/mutagen/embed/mutagen.gz (task-9-review.md
// Finding 1 — the full, in-order 14-value enum):
//
//	Disconnected, HaltedOnRootEmptied, HaltedOnRootDeletion,
//	HaltedOnRootTypeChange, ConnectingAlpha, ConnectingBeta, Watching,
//	Scanning, WaitingForRescan, Reconciling, StagingAlpha, StagingBeta,
//	Transitioning, Saving
//
// Only the first four are real failures; every other value is an
// ordinary phase of a normal sync cycle (connect → scan → reconcile →
// stage → transition → save → watch) — a real file edit drives every
// session through most of these on every cycle.
//
// This is a denylist of failures, not an allowlist of healthy values,
// deliberately: Task 9's original allowlist omitted five of the ten
// normal phases (StagingAlpha among them) and made ordinary syncing
// flap MutagenDead — see Task 9's review. A denylist means a mutagen
// version that adds an eleventh normal phase defaults to healthy
// (matching mutagen's own intent for the value) instead of unhealthy
// until someone updates this set. See statusToMutagenHealth for how
// an unrecognized value is still surfaced, just not as unhealthy.
var mutagenFailedStatuses = map[string]bool{
	"Disconnected":           true,
	"HaltedOnRootEmptied":    true,
	"HaltedOnRootDeletion":   true,
	"HaltedOnRootTypeChange": true,
}

// mutagenKnownHealthyStatuses are the remaining ten values of the same
// enum. It plays no role in the health decision itself (that's
// mutagenFailedStatuses, above) — it exists only so
// statusToMutagenHealth can tell "a status we've confirmed is a
// normal phase" apart from "a status outside both known sets", so the
// latter can be logged once as a signal that mutagen's enum has
// drifted and this file may need updating.
var mutagenKnownHealthyStatuses = map[string]bool{
	"ConnectingAlpha":  true,
	"ConnectingBeta":   true,
	"Watching":         true,
	"Scanning":         true,
	"WaitingForRescan": true,
	"Reconciling":      true,
	"StagingAlpha":     true,
	"StagingBeta":      true,
	"Transitioning":    true,
	"Saving":           true,
}

// loggedUnknownStatuses dedupes the one-time warning in
// statusToMutagenHealth by status value, so a status mutagen keeps
// emitting (across many sessions, or many ticks) produces one log
// line total, not one per occurrence.
var loggedUnknownStatuses sync.Map

// statusToMutagenHealth maps one session's .Status string to the
// cache's MutagenHealth shape using mutagenFailedStatuses as a
// denylist: a confirmed failure status maps to MutagenDead, and
// everything else — including a value outside both
// mutagenFailedStatuses and mutagenKnownHealthyStatuses — maps to
// MutagenOK. A status outside both known sets is logged once via
// daemonlog.Warnf (an "expected-but-noteworthy" event, not a failure
// — see internal/daemonlog's Warnf doc) so an operator notices the
// drift and can extend mutagenFailedStatuses or
// mutagenKnownHealthyStatuses; it fails open to healthy rather than
// unhealthy because a newly observed value is far more likely to be a
// new normal phase than a new failure mode, and flapping every
// session unhealthy on every enum addition is exactly the false-
// positive failure mode this fix closes for the known statuses.
func statusToMutagenHealth(status string) MutagenHealth {
	if mutagenFailedStatuses[status] {
		return MutagenHealth{Status: MutagenDead}
	}
	if !mutagenKnownHealthyStatuses[status] {
		if _, seen := loggedUnknownStatuses.LoadOrStore(status, true); !seen {
			daemonlog.Warnf("serviceapi: mutagen monitor: unrecognized session status %q, treating as healthy (denylist-based) — add to mutagenKnownHealthyStatuses or mutagenFailedStatuses if this is a real state", status)
		}
	}
	return MutagenHealth{Status: MutagenOK}
}
