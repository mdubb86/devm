package watchdog

import (
	"context"

	"github.com/mdubb86/devm/internal/daemonlog"
	"github.com/mdubb86/devm/internal/serviceapi"
)

type popCheck struct{}

func NewPopCheck() Check { return &popCheck{} }

func (popCheck) Name() string { return "pop-session" }

func (popCheck) Run(ctx context.Context, cache *serviceapi.StateCache, gt GroundTruth) (bool, error) {
	driftedAny := false
	for _, projectID := range gt.KnownProjectNames() {
		expected, _ := cache.ProjectRow(projectID)
		observed := gt.PopSessionSummaryForProject(projectID)
		if observed == expected.PopSessions {
			cache.TouchProjectReconciled(projectID)
			continue
		}
		driftedAny = true
		daemonlog.Warnf("watchdog: drift on pop-session for %s: count %d→%d oldest %d→%d, cache reconciled to observed",
			projectID, expected.PopSessions.Count, observed.Count, expected.PopSessions.OldestAgeSeconds, observed.OldestAgeSeconds)
		cache.SetPopSessionSummary(projectID, observed)
		cache.TouchProjectReconciled(projectID)
	}
	return driftedAny, nil
}
