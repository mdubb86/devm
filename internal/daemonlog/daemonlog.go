// Package daemonlog is the devm daemon's error-severity log sink.
//
// # Severity split
//
// The daemon separates informational lifecycle events from actual
// errors along the classical UNIX stdout/stderr split:
//
//   - Info / lifecycle events → stdout via stdlib `log.Printf` (the
//     daemon's main sets `log.SetOutput(os.Stdout)` at startup).
//     Examples: "listener bound", "vm cold-started", "iron-proxy
//     respawned", "reap orphan softnets: killed N".
//
//   - Errors → stderr via [Errorf]. Each error line carries the
//     current goroutine's stack trace so an operator diagnosing a
//     3am incident can see which goroutine surfaced the error and
//     what it was doing.
//
// Rationale: with everything on stderr (Go's stdlib default) a
// silent daemon is our recurring failure mode — real errors get
// buried in informational noise, and per-event categorization at
// grep time is guesswork. Splitting by writer lets `tail -f the
// .err.log` be a real error monitor.
//
// # Where to use each
//
// Reach for [Errorf] when:
//
//   - A goroutine exited unexpectedly (Serve returned, a watchdog
//     died) — the daemon is now in a degraded state.
//   - An operation you can't retry or recover from failed
//     ("supervisor stop for %s: %v" while cleaning up).
//   - An invariant was broken (missing snapshot, unexpected nil,
//     state a code path expected to find).
//
// Keep on stdlib `log.Printf` when:
//
//   - Reporting normal lifecycle progress ("listening on", "cold-
//     start done", "iron-proxy watchdog: respawned").
//   - A best-effort side operation succeeded ("iron-proxy adopt: N
//     recovered").
//
// A "continuing" (fail-soft) code path is still an error worth a
// stack trace — you swallowed a failure to keep going, but the
// swallowed failure is exactly what a future incident forensics
// needs to explain. Use [Errorf] there too.
package daemonlog

import (
	"fmt"
	"log"
	"os"
	"runtime/debug"
)

// stderrLog is the destination for [Errorf]. Separate from the
// stdlib default logger (which the daemon's main routes to
// os.Stdout) so information events and errors land in different
// launchd files (.out.log vs .err.log).
var stderrLog = log.New(os.Stderr, "", log.LstdFlags)

// Errorf logs an error to stderr with the current goroutine's
// stack trace appended. Format string is the same shape as
// [log.Printf]. Callers should include enough context in the
// message that the operator doesn't need the stack to identify
// WHICH operation failed — the stack answers WHERE in the code
// path, not WHAT the operator was trying to do.
func Errorf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	stderrLog.Printf("%s\ngoroutine stack:\n%s", msg, debug.Stack())
}
