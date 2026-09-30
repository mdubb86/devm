package serviceapi

import (
	"context"
	"fmt"
	"time"

	"github.com/mdubb86/devm/internal/daemonlog"
)

// proxyListenerRetryDelay is how long Run waits before re-probing a
// project whose first health check failed. A softnet reconfigure can
// cut the transport path for a brief window (Review Focus #3); one
// retry absorbs that blip so a transient probe failure doesn't
// respawn an otherwise healthy listener pair. Overridden in tests.
var proxyListenerRetryDelay = 500 * time.Millisecond

// proxyListenerSleep is the sleep hook proxyListenerRetryDelay waits
// on. Swapped for a no-op in tests so the retry path runs instantly.
var proxyListenerSleep = time.Sleep

type proxyListenerCheck struct{}

func NewProxyListenerCheck() Check { return &proxyListenerCheck{} }

func (proxyListenerCheck) Name() string { return "proxy-listener" }

// Run probes each running project's reverse-proxy listener pair via
// its reserved _devm.<project>.test route. A failed probe gets one
// retry before being treated as drift; drift respawns the project's
// :80/:443 listeners via Stop + Start. The cache always records the
// final probe result — even when the respawn itself failed — so
// /status reflects observed reality rather than what was expected.
func (proxyListenerCheck) Run(ctx context.Context, cache *StateCache, gt GroundTruth) (bool, error) {
	driftedAny := false
	var firstErr error
	for _, projectID := range gt.KnownProjectNames() {
		row, _ := cache.ProjectRow(projectID)
		if row.VMState != VMRunning {
			continue
		}

		healthy := gt.ProxyListenerHealth(ctx, projectID)
		if !healthy {
			proxyListenerSleep(proxyListenerRetryDelay)
			healthy = gt.ProxyListenerHealth(ctx, projectID)
		}

		if !healthy {
			driftedAny = true
			if err := gt.RespawnProxyListeners(ctx, projectID); err != nil {
				daemonlog.Errorf("watchdog: drift on proxy-listener for %s: repair failed: %v", projectID, err)
				if firstErr == nil {
					firstErr = fmt.Errorf("proxy-listener check: project %s: %w", projectID, err)
				}
			} else {
				daemonlog.Warnf("watchdog: drift on proxy-listener for %s: healthy → false, repaired", projectID)
			}
		}

		cache.SetProxyListenerHealth(projectID, healthy)
		cache.TouchProjectReconciled(projectID)
	}
	return driftedAny, firstErr
}
