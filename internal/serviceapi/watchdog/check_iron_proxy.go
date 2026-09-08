package watchdog

import (
	"context"
	"fmt"

	"github.com/mdubb86/devm/internal/daemonlog"
	"github.com/mdubb86/devm/internal/serviceapi"
)

type ironProxyCheck struct{}

func NewIronProxyCheck() Check { return &ironProxyCheck{} }

func (ironProxyCheck) Name() string { return "iron-proxy" }

func (ironProxyCheck) Run(ctx context.Context, cache *serviceapi.StateCache, gt GroundTruth) (bool, error) {
	driftedAny := false
	var firstErr error
	for _, projectID := range gt.KnownProjectNames() {
		expected, _ := cache.ProjectRow(projectID)
		observed := gt.IronProxyHealth(ctx, projectID)
		if observed.Status == expected.IronProxyHealth.Status {
			cache.TouchProjectReconciled(projectID)
			continue
		}
		driftedAny = true
		var repairErr error
		if observed.Status == serviceapi.ProxyMissing {
			if err := gt.RespawnIronProxy(ctx, projectID); err != nil {
				repairErr = err
				daemonlog.Errorf("watchdog: drift on iron-proxy for %s: repair failed: %v",
					projectID, err)
			} else {
				observed = gt.IronProxyHealth(ctx, projectID)
				daemonlog.Warnf("watchdog: drift on iron-proxy for %s: %s → %s, repaired",
					projectID, expected.IronProxyHealth.Status, observed.Status)
			}
		} else {
			daemonlog.Warnf("watchdog: drift on iron-proxy for %s: %s → %s, cache reconciled to observed",
				projectID, expected.IronProxyHealth.Status, observed.Status)
		}
		cache.SetIronProxyHealth(projectID, observed)
		cache.TouchProjectReconciled(projectID)
		if repairErr != nil && firstErr == nil {
			firstErr = fmt.Errorf("iron-proxy check: project %s: %w", projectID, repairErr)
		}
	}
	return driftedAny, firstErr
}
