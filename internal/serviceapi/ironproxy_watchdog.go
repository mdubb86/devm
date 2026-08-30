package serviceapi

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/mdubb86/devm/internal/daemonlog"
	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/supervisor"
)

// ironProxyWatchdogInterval is how often the watchdog checks each
// running project's iron-proxy for health. Short enough that a Claude
// session inside the guest notices only a brief DNS/egress interruption
// when iron-proxy dies unexpectedly (SIGKILL, hard crash — anything the
// setsid shim's session-detach doesn't cover); long enough to keep the
// per-tick snapshot reads out of the CPU profile.
const ironProxyWatchdogInterval = 30 * time.Second

// runIronProxyWatchdog polls known running projects and respawns any
// iron-proxy that has silently died. Blocks until ctx is done; per-project
// respawn failures are logged and swallowed so one flaky project doesn't
// starve the others.
//
// Secret-injecting projects are respawned like any other: secrets live
// in the on-disk file store (secret.NewFileBackend), which the daemon
// reads directly via rebuildIronProxyConfig — no CLI round-trip needed.
func runIronProxyWatchdog(
	ctx context.Context,
	cfg identity.Config,
	sup *supervisor.Supervisor,
	proxy *ProxyServer,
	locks *ProjectLocks,
) error {
	tick := time.NewTicker(ironProxyWatchdogInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			healIronProxies(ctx, cfg, sup, proxy, locks)
		}
	}
}

// healIronProxies is one watchdog pass — iterate ironProxyState's
// known-running projects and respawn from persisted state any whose
// iron-proxy is MISSING. Extracted from runIronProxyWatchdog so tests
// can drive one tick without a goroutine + ticker.
//
// ironProxyState.keys() only contains running projects (vm.go's
// /vm/stop calls ironProxyState.del after Stop), so a stopped VM
// won't be seen here.
func healIronProxies(
	ctx context.Context,
	cfg identity.Config,
	sup *supervisor.Supervisor,
	proxy *ProxyServer,
	locks *ProjectLocks,
) {
	for _, projectID := range ironProxyState.keys() {
		health := computeProxyHealth(cfg, sup, proxy, projectID)
		if health.Status != ProxyMissing {
			continue
		}
		if err := respawnIronProxyFromState(ctx, cfg, sup, locks, projectID); err != nil {
			daemonlog.Errorf("serviceapi: iron-proxy watchdog: respawn %s: %v", projectID, err)
			continue
		}
		log.Printf("serviceapi: iron-proxy watchdog: respawned %s (was missing)", projectID)
	}
}

// respawnIronProxyFromState rebuilds an IronProxyConfig from the
// project's persisted state via rebuildIronProxyConfig and spawns a
// fresh iron-proxy — secret-injecting projects included, since
// rebuildIronProxyConfig resolves secret values straight from the
// on-disk file store.
//
// Acquires the project's reconcile lock so a watchdog respawn can't
// race a concurrent /vm/start or /vm/reconcile (both take the same
// lock in apply_iron_proxy.go and reconcile.go).
func respawnIronProxyFromState(
	ctx context.Context,
	cfg identity.Config,
	sup *supervisor.Supervisor,
	locks *ProjectLocks,
	projectID string,
) error {
	unlock := locks.Lock(projectID)
	defer unlock()

	// Re-check health under the lock — a /vm/start that raced us to
	// the lock may have already respawned iron-proxy, in which case
	// we'd otherwise stop+spawn again pointlessly.
	if computeProxyHealth(cfg, sup, proxy_nilForRecheck(), projectID).Status != ProxyMissing {
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
	return spawnIronProxyFn(ctx, cfg, sup, projectID, proxyCfg)
}

// proxy_nilForRecheck lets the recheck under the lock use a nil
// *ProxyServer intentionally — RebindStatus isn't relevant to the
// MISSING vs OK verdict, and passing nil sidesteps the need to plumb
// the ProxyServer through respawnIronProxyFromState just for the
// recheck. Naming keeps grep-searchability if this ever needs to
// pass a real proxy.
func proxy_nilForRecheck() *ProxyServer { return nil }
