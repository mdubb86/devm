package serviceapi

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mdubb86/devm/internal/docker"
	"github.com/mdubb86/devm/internal/supervisor"
)

// healNoSecretProxiesAtStartup respawns iron-proxies that are MISSING
// for no-secret projects whose VM is currently running. Runs once at
// daemon startup, after AdoptIronProxies — that call only re-attaches
// iron-proxies still alive from a prior daemon instance; this call
// covers the gap where the proxy process itself is gone (crashed,
// killed) but the VM kept running.
//
// Secret-bearing projects are skipped: the daemon has no keychain
// access outside a user-context CLI invocation, so those wait for the
// CLI to notice (via /handshake) and call HealIronProxy.
//
// The daemon has no live index from a running VM's name to its project
// id — VMName is caller-supplied per request, not derivable from the
// VM itself. Instead this walks every persisted StateSnapshot (one per
// project the daemon has ever seen a cold-start for) and matches its
// Cfg.Project.VMName against the running VM set from tr.List.
//
// Best-effort throughout: any failure (listing VMs, reading state,
// resolving ports, spawning) is logged to stderr for that project and
// never aborts startup or blocks healing the remaining projects.
func healNoSecretProxiesAtStartup(ctx context.Context, tr TartLister, sup *supervisor.Supervisor, denials *Denials) {
	vms, err := tr.List(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "startup heal: tart list: %v\n", err)
		return
	}
	running := make(map[string]bool, len(vms))
	for _, vm := range vms {
		if vm.Running {
			running[vm.Name] = true
		}
	}
	if len(running) == 0 {
		return
	}

	entries, err := os.ReadDir(StateDir())
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "startup heal: read state dir: %v\n", err)
		}
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		projectID := strings.TrimSuffix(name, ".json")

		snap, err := ReadStateSnapshot(projectID)
		if err != nil || snap == nil {
			continue
		}
		if !running[snap.Cfg.Project.VMName] {
			continue
		}
		if cfgHasSecretRefs(snap.Cfg) {
			continue
		}
		if computeProxyHealth(sup, projectID).Status != ProxyMissing {
			continue
		}
		if err := respawnNoSecretProxy(ctx, sup, projectID, snap, denials); err != nil {
			fmt.Fprintf(os.Stderr, "startup heal: project %q: %v\n", projectID, err)
		}
	}
}

// respawnNoSecretProxy rebuilds the IronProxyConfig for projectID from
// the on-disk iron-proxy config's MAC_HOST + ports (same shape
// RegisterApplyIronProxyHandler uses in apply_iron_proxy.go) plus the
// snapshot's effective allowlist, spawns it, and stamps the snapshot's
// ProxyVersion so a later STALE check treats this proxy as current.
//
// No config file (VM never started an iron-proxy for this project)
// surfaces here as an error from loadIronProxyInfoFromConfig — the
// caller logs and moves on, which is the same "nothing to heal" outcome
// apply_iron_proxy.go treats as a deliberate no-op.
func respawnNoSecretProxy(ctx context.Context, sup *supervisor.Supervisor, projectID string, snap *StateSnapshot, denials *Denials) error {
	cfgPath, err := IronProxyConfigPath(projectID)
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}
	info, err := loadIronProxyInfoFromConfig(cfgPath)
	if err != nil {
		return fmt.Errorf("read iron-proxy config: %w", err)
	}
	caDir, err := EnsureRuntimeDir()
	if err != nil {
		return err
	}
	newCfg := IronProxyConfig{
		HTTPListen:  fmt.Sprintf("%s:%d", info.MacHost, info.HTTPPort),
		HTTPSListen: fmt.Sprintf("%s:%d", info.MacHost, info.HTTPSPort),
		DNSListen:   fmt.Sprintf("%s:%d", info.MacHost, info.DNSPort),
		DNSProxyIP:  proxySentinelIP,
		CACertPath:  filepath.Join(caDir, "ca", "root.crt"),
		CAKeyPath:   filepath.Join(caDir, "ca", "root.key"),
		AllowList:   docker.EffectiveAllowlist(snap.Cfg),
	}
	if err := spawnIronProxyFn(ctx, sup, projectID, newCfg, denials); err != nil {
		return fmt.Errorf("spawn iron-proxy: %w", err)
	}
	return stampProxyVersion(projectID)
}
