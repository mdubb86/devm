package serviceapi

import (
	"context"
	"fmt"

	"github.com/mdubb86/devm/internal/daemonlog"
)

type mutagenCheck struct{}

func NewMutagenCheck() Check { return &mutagenCheck{} }

func (mutagenCheck) Name() string { return "mutagen" }

func (mutagenCheck) Run(ctx context.Context, cache *StateCache, gt GroundTruth) (bool, error) {
	expectedPID := cache.Global().MutagenDaemonPID
	observed, err := gt.MutagenLockPID(gt.MutagenDataDir())
	if err != nil {
		daemonlog.Errorf("watchdog: mutagen check: observe error: %v", err)
		cache.TouchGlobalReconciled()
		return false, fmt.Errorf("mutagen check: %w", err)
	}
	if observed == expectedPID && observed != 0 {
		cache.TouchGlobalReconciled()
		for _, p := range gt.KnownProjectNames() {
			cache.TouchProjectReconciled(p)
		}
		return false, nil
	}

	var repairErr error
	finalPID := observed
	if observed == 0 {
		if err := gt.RespawnMutagenDaemon(ctx); err != nil {
			repairErr = err
			daemonlog.Errorf("watchdog: drift on mutagen: repair failed: %v", err)
		} else {
			reObserved, obsErr := gt.MutagenLockPID(gt.MutagenDataDir())
			if obsErr == nil {
				finalPID = reObserved
			}
			daemonlog.Warnf("watchdog: drift on mutagen: pid %d → %d, repaired", expectedPID, finalPID)
		}
	} else {
		daemonlog.Warnf("watchdog: drift on mutagen: pid %d → %d, cache reconciled to observed", expectedPID, observed)
	}

	cache.SetMutagenDaemonPID(finalPID)
	// Per-project MutagenHealth is otherwise owned exclusively by the
	// mutagen monitor subscriber (mutagen_monitor.go), which observes
	// each session's real transport status within its own tick
	// cadence (seconds) rather than this check's 60s poll. The one
	// exception: when the daemon itself is confirmed gone (finalPID
	// == 0, including a failed respawn), every project's session is
	// definitionally disconnected too — the subscriber's own
	// subprocess has nothing to connect to either — so that fact is
	// still forced here rather than left stale until the subscriber's
	// next reconnect attempt notices.
	if finalPID == 0 {
		for _, p := range gt.KnownProjectNames() {
			cache.SetMutagenHealth(p, MutagenHealth{Status: MutagenDead})
		}
	}
	for _, p := range gt.KnownProjectNames() {
		cache.TouchProjectReconciled(p)
	}
	cache.TouchGlobalReconciled()

	if repairErr != nil {
		return true, fmt.Errorf("mutagen check: %w", repairErr)
	}
	return true, nil
}
