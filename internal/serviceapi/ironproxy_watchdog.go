package serviceapi

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
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
// Only projects that don't inject secrets are auto-respawned. Secret
// values never persist to disk — they arrive on the CLI reconcile path
// — so the daemon can't rebuild a secret-injecting iron-proxy config on
// its own. Those projects get a log line pointing at `devm reconcile`
// and are otherwise left for the user to heal.
func runIronProxyWatchdog(
	ctx context.Context,
	cfg identity.Config,
	sup *supervisor.Supervisor,
	proxy *ProxyServer,
	locks *ProjectLocks,
	denials *Denials,
) error {
	tick := time.NewTicker(ironProxyWatchdogInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			healIronProxies(ctx, cfg, sup, proxy, locks, denials)
		}
	}
}

// healIronProxies is one watchdog pass — iterate ironProxyState's
// known-running projects, and for any where iron-proxy is MISSING and
// no secret injection is required, respawn from persisted state.
// Extracted from runIronProxyWatchdog so tests can drive one tick
// without a goroutine + ticker.
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
	denials *Denials,
) {
	for _, projectID := range ironProxyState.keys() {
		health := computeProxyHealth(cfg, sup, proxy, projectID)
		if health.Status != ProxyMissing {
			continue
		}
		if health.NeedsSecrets {
			log.Printf("serviceapi: iron-proxy watchdog: %s missing but injects secrets; skipping (run 'devm reconcile' to heal)", projectID)
			continue
		}
		if err := respawnIronProxyFromState(ctx, cfg, sup, locks, denials, projectID); err != nil {
			daemonlog.Errorf("serviceapi: iron-proxy watchdog: respawn %s: %v", projectID, err)
			continue
		}
		log.Printf("serviceapi: iron-proxy watchdog: respawned %s (was missing)", projectID)
	}
}

// respawnIronProxyFromState rebuilds an IronProxyConfig from the
// project's persisted state (on-disk YAML for ports, state snapshot for
// the network allowlist, runtime dir for CA paths) and spawns a fresh
// iron-proxy. Empty Secrets by design — callers gate on cfgHasSecretRefs
// before invoking this.
//
// Acquires the project's reconcile lock so a watchdog respawn can't
// race a concurrent /vm/start or /vm/reconcile (both take the same
// lock in apply_iron_proxy.go and reconcile.go).
func respawnIronProxyFromState(
	ctx context.Context,
	cfg identity.Config,
	sup *supervisor.Supervisor,
	locks *ProjectLocks,
	denials *Denials,
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

	cfgPath, err := IronProxyConfigPath(cfg, projectID)
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}
	diskInfo, err := loadIronProxyInfoFromConfig(cfgPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("no prior iron-proxy config on disk (project may be stopping)")
		}
		return fmt.Errorf("load prior config: %w", err)
	}
	snap, err := ReadStateSnapshot(cfg, projectID)
	if err != nil {
		return fmt.Errorf("read snapshot: %w", err)
	}
	if snap == nil {
		return errors.New("no state snapshot")
	}
	caDir, err := EnsureRuntimeDir(cfg)
	if err != nil {
		return fmt.Errorf("runtime dir: %w", err)
	}

	proxyCfg := IronProxyConfig{
		HTTPListen:  ironProxyListenAddr(diskInfo.HTTPPort),
		HTTPSListen: ironProxyListenAddr(diskInfo.HTTPSPort),
		DNSListen:   ironProxyListenAddr(diskInfo.DNSPort),
		DNSProxyIP:  interceptedEgressIP,
		CACertPath:  filepath.Join(caDir, "ca", "root.crt"),
		CAKeyPath:   filepath.Join(caDir, "ca", "root.key"),
		AllowList:   snap.Cfg.Network.Domains(),
	}
	return spawnIronProxyFn(ctx, cfg, sup, projectID, proxyCfg, denials)
}

// proxy_nilForRecheck lets the recheck under the lock use a nil
// *ProxyServer intentionally — RebindStatus isn't relevant to the
// MISSING vs OK verdict, and passing nil sidesteps the need to plumb
// the ProxyServer through respawnIronProxyFromState just for the
// recheck. Naming keeps grep-searchability if this ever needs to
// pass a real proxy.
func proxy_nilForRecheck() *ProxyServer { return nil }
