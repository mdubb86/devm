package serviceapi

import (
	"github.com/mdubb86/devm/internal/daemonlog"
	"github.com/mdubb86/devm/internal/identity"
)

// rehydrateCacheFromStateSnapshots restores per-project cache fields
// from persisted StateSnapshot files on daemon-restart adopt. Only
// fields that were written once at /vm/start and never re-derived by
// the watchdog need this — everything the watchdog reconciles
// (VMState, IronProxyHealth, MutagenHealth, ApproveState) gets
// refreshed on the next RunOnce and doesn't belong here.
//
// Currently that's just MacCwd. The propose/passthrough/approve
// handlers reject requests with 412 "project not started" when
// cache.ProjectRow.MacCwd is empty, so a running-VM project whose
// daemon was restarted becomes silently unusable for guest-initiated
// flows without this rehydrate — every gdevm propose / passthrough
// call fails on the precondition check even though everything else
// (VM, iron-proxy, softnet listeners) is in place.
//
// Best-effort per project: a missing or unparseable snapshot logs and
// the project keeps running with an empty MacCwd — the user recovers
// via `devm start` from the project dir, same as the original
// /vm/start path.
func rehydrateCacheFromStateSnapshots(cfg identity.Config, cache *StateCache, projectIDs []string) {
	for _, id := range projectIDs {
		snap, err := ReadStateSnapshot(cfg, id)
		if err != nil {
			daemonlog.Errorf("serviceapi: adopt cache rehydrate: read state for %s: %v", id, err)
			continue
		}
		if snap == nil {
			continue
		}
		if snap.MacCwd != "" {
			cache.SetMacCwd(id, snap.MacCwd)
		}
	}
}
