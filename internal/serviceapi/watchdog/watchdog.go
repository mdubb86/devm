// Package watchdog is the daemon's state reconciler. See
// docs/superpowers/specs/2026-09-08-state-cache-watchdog-design.md.
//
// The StateWatchdog runs a structured check set every 60s: each check
// reads expected state from the StateCache, observes ground truth via
// the GroundTruth interface, on drift applies its repair policy (or
// updates the cache to reflect reality), and touches the row's
// LastReconciledAt. Warmup at boot fires the same checks once,
// synchronously, before the HTTP server accepts its first connection.
package watchdog
