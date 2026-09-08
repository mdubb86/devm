package serviceapi

import (
	"context"
	"time"

	"github.com/mdubb86/devm/internal/daemonlog"
)

type approveCheck struct{}

func NewApproveCheck() Check { return &approveCheck{} }

func (approveCheck) Name() string { return "approve-state" }

func (approveCheck) Run(ctx context.Context, cache *StateCache, gt GroundTruth) (bool, error) {
	driftedAny := false
	var firstErr error
	for _, projectID := range gt.KnownProjectNames() {
		expected, _ := cache.ProjectRow(projectID)
		if expected.MacCwd == "" {
			// Project not started yet — no files on the Mac side to hash.
			continue
		}

		currentDevm, currentMe, err := gt.ApproveHash(expected.MacCwd)
		if err != nil {
			daemonlog.Errorf("watchdog: approve check: observe hash for %s: %v", projectID, err)
			cache.TouchProjectReconciled(projectID)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		approvedDevm, approvedMe, since, hasSnap, err := gt.ReadApprovedSnapshot(projectID)
		if err != nil {
			daemonlog.Errorf("watchdog: approve check: observe snapshot for %s: %v", projectID, err)
			cache.TouchProjectReconciled(projectID)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}

		newSummary := ApproveStateSummary{
			CurrentDevmSHA:  currentDevm,
			CurrentMeSHA:    currentMe,
			ApprovedDevmSHA: approvedDevm,
			ApprovedMeSHA:   approvedMe,
			ApprovedSince:   since,
		}
		if !hasSnap {
			newSummary.Diverged = true
		} else {
			newSummary.Diverged = currentDevm != approvedDevm || currentMe != approvedMe
		}

		if approveStateEqual(expected.ApproveState, newSummary) {
			cache.TouchProjectReconciled(projectID)
			continue
		}

		driftedAny = true
		daemonlog.Warnf("watchdog: drift on approve-state for %s: diverged %v → %v, cache reconciled to observed",
			projectID, expected.ApproveState.Diverged, newSummary.Diverged)
		cache.SetApproveState(projectID, newSummary)
		cache.TouchProjectReconciled(projectID)
	}
	return driftedAny, firstErr
}

func approveStateEqual(a, b ApproveStateSummary) bool {
	return a.Diverged == b.Diverged &&
		a.CurrentDevmSHA == b.CurrentDevmSHA &&
		a.ApprovedDevmSHA == b.ApprovedDevmSHA &&
		a.CurrentMeSHA == b.CurrentMeSHA &&
		a.ApprovedMeSHA == b.ApprovedMeSHA &&
		timeEq(a.ApprovedSince, b.ApprovedSince)
}

func timeEq(a, b *time.Time) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.Equal(*b)
}
