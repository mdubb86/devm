package serviceapi

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/supervisor"
)

// RespawnIronProxyForWatchdog rebuilds an IronProxyConfig from the
// project's persisted state via rebuildIronProxyConfig and spawns a
// fresh iron-proxy — secret-injecting projects included, since
// rebuildIronProxyConfig resolves secret values straight from the
// on-disk file store.
//
// Acquires the project's reconcile lock so a watchdog respawn can't
// race a concurrent /vm/start or /vm/reconcile (both take the same
// lock in apply_iron_proxy.go and reconcile.go).
func RespawnIronProxyForWatchdog(
	ctx context.Context,
	cfg identity.Config,
	sup *supervisor.Supervisor,
	proxy *ProxyServer,
	projectID string,
	locks *ProjectLocks,
) error {
	unlock := locks.Lock(projectID)
	defer unlock()

	// Re-check health under the lock — a /vm/start that raced us to
	// the lock may have already respawned iron-proxy, in which case
	// we'd otherwise stop+spawn again pointlessly.
	if ComputeProxyHealth(cfg, sup, proxy, projectID).Status != ProxyMissing {
		return nil
	}

	snap, err := ReadStateSnapshot(cfg, projectID)
	if err != nil {
		return fmt.Errorf("read snapshot: %w", err)
	}
	if snap == nil {
		return errors.New("no state snapshot")
	}

	proxyCfg, err := rebuildIronProxyConfig(cfg, projectID, snap.Cfg, snap.MacCwd)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("no prior iron-proxy config on disk (project may be stopping)")
		}
		return fmt.Errorf("rebuild config: %w", err)
	}
	// cache is nil here: the watchdog check that calls this repair path
	// already writes the reconciled ProxyHealth into the cache itself
	// right after this returns (watchdog_check_iron_proxy.go) — this
	// respawn only needs the OnUnexpectedExit hook for a LATER crash of
	// the freshly spawned proxy, which the next watchdog tick still
	// catches.
	return spawnIronProxyFn(ctx, cfg, sup, projectID, proxyCfg, nil)
}
