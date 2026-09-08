// StateWatchdog is the daemon's state reconciler. See
// docs/superpowers/specs/2026-09-08-state-cache-watchdog-design.md.
//
// It runs a structured check set every 60s: each check reads expected
// state from the StateCache, observes ground truth via the
// GroundTruth interface, on drift applies its repair policy (or
// updates the cache to reflect reality), and touches the row's
// LastReconciledAt. Warmup at boot fires the same checks once,
// synchronously, before the HTTP server accepts its first connection.
package serviceapi

import (
	"context"
	"log"
	"time"

	"github.com/mdubb86/devm/internal/daemonlog"
)

// StateWatchdog runs a fixed set of Checks on a tick, reconciling the
// StateCache against ground truth.
type StateWatchdog struct {
	cache  *StateCache
	gt     GroundTruth
	checks []Check
	tick   time.Duration
}

// NewStateWatchdog builds a StateWatchdog over checks, firing every
// tick when run via Run.
func NewStateWatchdog(cache *StateCache, gt GroundTruth, checks []Check, tick time.Duration) *StateWatchdog {
	return &StateWatchdog{cache: cache, gt: gt, checks: checks, tick: tick}
}

// RunOnce runs every check exactly once, aggregating the drift count.
// Used for warmup at boot and for each ticker fire in Run. A check
// error is logged and does not stop the remaining checks from running.
func (w *StateWatchdog) RunOnce(ctx context.Context) (drifts int) {
	start := time.Now()
	for _, c := range w.checks {
		drifted, err := c.Run(ctx, w.cache, w.gt)
		if err != nil {
			daemonlog.Errorf("watchdog: check %s errored: %v", c.Name(), err)
		}
		if drifted {
			drifts++
		}
	}
	log.Printf("watchdog: pass complete in %s, %d drifts detected", time.Since(start), drifts)
	return drifts
}

// Run is the long-running actor: it fires RunOnce every w.tick until
// ctx is cancelled, at which point it returns ctx.Err().
func (w *StateWatchdog) Run(ctx context.Context) error {
	t := time.NewTicker(w.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			w.RunOnce(ctx)
		}
	}
}
