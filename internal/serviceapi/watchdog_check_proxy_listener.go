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

// proxyListenerSleep is the sleep hook proxyListenerRetryDelay and
// proxyListenerRespawnRecheckDelay wait on. Swapped for a no-op in
// tests so the retry/recheck paths run instantly.
var proxyListenerSleep = time.Sleep

// proxyListenerRespawnRecheckDelay is how long Run waits after a
// successful respawn before re-probing once, so a self-healed listener
// pair is reflected in the cache on the same tick instead of showing
// unhealthy for up to one full watchdog interval. Overridden in tests.
var proxyListenerRespawnRecheckDelay = 250 * time.Millisecond

type proxyListenerCheck struct{}

func NewProxyListenerCheck() Check { return &proxyListenerCheck{} }

func (proxyListenerCheck) Name() string { return "proxy-listener" }

// Run probes each running project's reverse-proxy listener pair via
// its reserved _devm.<project>.test route. A failed probe gets one
// retry before being treated as drift; drift respawns the project's
// :80/:443 listeners via Stop + Start. After a successful respawn, Run
// re-probes once more (after proxyListenerRespawnRecheckDelay) so a
// self-healed listener pair is reflected in the cache on the same
// tick rather than showing unhealthy for up to one full watchdog
// interval. The cache always records the final probe result — even
// when the respawn itself failed — so /status reflects observed
// reality rather than what was expected.
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
				proxyListenerSleep(proxyListenerRespawnRecheckDelay)
				healthy = gt.ProxyListenerHealth(ctx, projectID)
				if healthy {
					daemonlog.Warnf("watchdog: drift on proxy-listener for %s: healthy → false, respawned and recovered", projectID)
				} else {
					daemonlog.Warnf("watchdog: drift on proxy-listener for %s: healthy → false, respawned but still unhealthy", projectID)
				}
			}
		}

		cache.SetProxyListenerHealth(projectID, healthy)
		cache.TouchProjectReconciled(projectID)
	}
	return driftedAny, firstErr
}
