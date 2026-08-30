package serviceapi

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/mdubb86/devm/internal/daemonlog"
	"github.com/mdubb86/devm/internal/docker"
	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/ironproxy"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/supervisor"
)

// DiscoveredIronProxy is one running iron-proxy process the daemon has
// found on startup: its PID and the project it serves. The config file
// path isn't stored here — it's derivable from ProjectID via
// IronProxyConfigPath, so we don't rely on parsing it out of `ps`
// output (paths under macOS's ~/Library/Application Support/ contain a
// space, and ps -axo command doesn't quote argv). The daemon reads
// that config file back at adopt time to rehydrate ironProxyState.
type DiscoveredIronProxy struct {
	PID       int
	ProjectID string
}

// DiscoverIronProxies returns every running iron-proxy process whose
// binary path matches the one this daemon would launch, paired with
// its project id and the on-disk config file it was launched with.
//
// Spawned iron-proxies survive daemon death because SpawnIronProxy
// wraps them in the devm-setsid-shim, which starts iron-proxy in a
// new session detached from the daemon's process tree — see
// internal/setsidshim. Without re-attachment, a daemon restart would
// leave them running but unmanaged — stop/status would say "not
// present" while the process kept enforcing egress. Discovery on
// startup closes that gap.
//
// Matching is intentionally strict: the command must start with the
// canonical iron-proxy binary path. We never adopt unrelated processes.
func DiscoverIronProxies(ctx context.Context, cfg identity.Config) ([]DiscoveredIronProxy, error) {
	runDir, err := EnsureRuntimeDir(cfg)
	if err != nil {
		return nil, fmt.Errorf("runtime dir: %w", err)
	}
	binary, err := ironproxy.Ensure(runDir)
	if err != nil {
		return nil, fmt.Errorf("locate iron-proxy: %w", err)
	}
	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	return parseIronProxyProcesses(string(out), binary), nil
}

// AdoptIronProxies discovers running iron-proxies, registers each with
// the supervisor as adopted, and rehydrates ironProxyState from each
// process's on-disk config file so per-project handlers
// (/vm/enforcement-config) keep working across a daemon restart.
//
// The config file is the authoritative record of the ports iron-proxy
// bound at spawn time; without rehydration, ironProxyState is empty at
// daemon startup and a subsequent /vm/start would pick fresh ports
// while the running VM's dnsmasq still points at the old ones —
// silent DNS failure inside the guest.
//
// Best-effort per-process: a project whose config file is missing,
// unreadable, or malformed is adopted (so /vm/stop still finds it) but
// left out of ironProxyState. The bump-in-the-log is via the returned
// error only if the discovery step itself failed; per-process
// rehydrate failures are swallowed silently to match the "best-effort"
// contract callers expect.
//
// Beyond rehydrating ironProxyState, each recovered project also gets
// its SSH host port and allocated project IP restored, and its direct
// routes rebuilt, from the on-disk state snapshot (recoverProjectState)
// — all of that is in-memory-only and otherwise lost on daemon
// restart, breaking ingress/DNS for a VM that's still running under an
// orphaned iron-proxy.
func AdoptIronProxies(ctx context.Context, cfg identity.Config, sup *supervisor.Supervisor, tr *tart.Tart, routes *Routes) error {
	procs, err := DiscoverIronProxies(ctx, cfg)
	if err != nil {
		return err
	}
	for _, p := range procs {
		adoptOneIronProxy(ctx, cfg, sup, tr, routes, p)
	}
	return nil
}

// adoptOneIronProxy is AdoptIronProxies's per-process body, split out so
// it can be unit tested without shelling out to `ps` (DiscoverIronProxies).
//
// A failure to rehydrate ironProxyState from the on-disk config (file
// missing, unreadable, or malformed) is logged and otherwise swallowed —
// per the "best-effort" contract callers expect — but execution must
// still reach recoverProjectState. Skipping it (the previous behavior)
// left a running adopted VM's egress permanently fail-closed at 502:
// the policy socket is served only by recoverProjectState, and nothing
// else re-serves it after a daemon restart.
func adoptOneIronProxy(ctx context.Context, cfg identity.Config, sup *supervisor.Supervisor, tr *tart.Tart, routes *Routes, p DiscoveredIronProxy) {
	sup.Adopt(supervisor.Key{ProjectID: p.ProjectID, Role: supervisor.RoleProxy}, p.PID)
	_, hadEntry := ironProxyState.get(p.ProjectID)
	info, err := ironProxyInfoForAdopted(cfg, p.ProjectID)
	if err != nil {
		daemonlog.Errorf("adopt: rehydrate ports for %s: %v (ports were not recovered; policy is still served)", p.ProjectID, err)
	} else {
		ironProxyState.put(p.ProjectID, info)
	}
	recoverProjectState(ctx, cfg, tr, routes, p.ProjectID)
	if err != nil && !hadEntry {
		// recoverProjectState unconditionally seeds an ironProxyState
		// entry when a snapshot exists (so it has somewhere to merge a
		// restored ProjectIP into) — with no prior entry and config-load
		// having failed, that can leave a bare zero-value entry with no
		// ProjectIP either. Downstream consumers treat any ironProxyState
		// entry as "this project has a live iron-proxy": discoverSoftnet
		// would push a FORWARDING rule at HostLoopIP:0, and
		// healIronProxies' watchdog would see ProxyMissing and try to
		// kill+respawn a proxy that's actually running fine. Strip the
		// entry back out unless the snapshot contributed something
		// (ProjectIP) worth keeping — restores the pre-adoption "no
		// entry on unreadable config" property while still letting
		// policy re-serve happen above.
		if after, ok := ironProxyState.get(p.ProjectID); ok && after == (projectInfo{}) {
			ironProxyState.del(p.ProjectID)
		}
	}
}

// ironProxyInfoForAdopted reads back the ports iron-proxy was launched
// with from its on-disk config file at the path IronProxyConfigPath
// derives for projectID.
func ironProxyInfoForAdopted(cfg identity.Config, projectID string) (projectInfo, error) {
	path, err := IronProxyConfigPath(cfg, projectID)
	if err != nil {
		return projectInfo{}, err
	}
	return loadIronProxyInfoFromConfig(path)
}

// recoverProjectState rebuilds the parts of a recovered project's
// in-memory state that live outside ironProxyState's config-file
// rehydration: the allocated project IP (read back from the
// last-applied state snapshot, since it isn't part of iron-proxy's own
// config shape) and the project's full route set (replayed verbatim
// from snap.Routes, mirrored there on every /routes/apply). It's
// split out of AdoptIronProxies's loop so it can be unit tested
// without shelling out to `ps` (DiscoverIronProxies).
//
// Best-effort: a missing/malformed snapshot (or one written before
// snap.Routes existed) simply leaves nothing to recover — the user's
// next `devm shell` / `devm route local|vm` re-populates both the
// live table and the snapshot.
func recoverProjectState(ctx context.Context, cfg identity.Config, tr *tart.Tart, routes *Routes, projectID string) {
	snap, err := ReadStateSnapshot(cfg, projectID)
	if err != nil || snap == nil {
		return
	}

	// Re-serve the policy socket for the adopted iron-proxy. The
	// allowlist lives only in daemon memory (the grpc transform consults
	// the PolicyAuthority per request), so until this runs the adopted
	// proxy fail-closes every guest request with a 502. Recomputed from
	// the snapshot with the same composition SpawnIronProxy's callers
	// use; a recompute failure keeps the socket unserved — 502
	// fail-closed — rather than serving a wrong allowlist.
	repoHosts, err := RepoHosts(snap.Cfg, snap.MacCwd)
	if err != nil {
		daemonlog.Errorf("policy: recover repo hosts for %s: %v (egress stays fail-closed)", projectID, err)
	} else if sockPath, err := IronPolicySocketPath(cfg, projectID); err != nil {
		daemonlog.Errorf("policy: socket path for %s: %v (egress stays fail-closed)", projectID, err)
	} else {
		policyAuthority.SetAllowlist(projectID, AppendUniqueHosts(docker.EffectiveAllowlist(snap.Cfg), repoHosts))
		policyAuthority.SetMode(projectID, ModeRestricted)
		if err := policyAuthority.EnsureServing(projectID, sockPath); err != nil {
			daemonlog.Errorf("policy: serve for adopted %s: %v (egress stays fail-closed)", projectID, err)
		}
	}

	info, _ := ironProxyState.get(projectID)
	// ProjectIP is not part of iron-proxy's on-disk config —
	// restore it from the snapshot too, or a daemon restart would
	// silently strand a running project without its allocated
	// 127.42.0.x address (AllocateProjectIP would then hand out a
	// second address for the same project on the next /vm/start).
	if snap.ProjectIP != "" {
		info.ProjectIP = snap.ProjectIP
	}
	ironProxyState.put(projectID, info)

	if len(snap.Routes) > 0 {
		if err := routes.Apply(projectID, snap.Routes); err != nil {
			daemonlog.Errorf("routes: recover routes for %s: %v (continuing)", projectID, err)
		}
	}
}

// parseIronProxyProcesses extracts iron-proxy entries from `ps -axo
// pid=,command=` output. Split out from DiscoverIronProxies so tests
// don't have to shell out.
func parseIronProxyProcesses(psOutput, ironProxyBinary string) []DiscoveredIronProxy {
	var out []DiscoveredIronProxy
	sc := bufio.NewScanner(strings.NewReader(psOutput))
	for sc.Scan() {
		line := strings.TrimLeft(sc.Text(), " ")
		if line == "" {
			continue
		}
		ws := strings.IndexAny(line, " \t")
		if ws < 0 {
			continue
		}
		pid, err := strconv.Atoi(line[:ws])
		if err != nil {
			continue
		}
		command := strings.TrimLeft(line[ws:], " \t")
		if !strings.HasPrefix(command, ironProxyBinary) {
			continue
		}
		projectID, ok := parseIronProxyProjectID(command)
		if !ok {
			continue
		}
		out = append(out, DiscoveredIronProxy{
			PID:       pid,
			ProjectID: projectID,
		})
	}
	return out
}

// parseIronProxyProjectID pulls the project id out of a command line
// like "/path/to/iron-proxy -config <runtime_dir>/iron-proxy/<id>.yaml".
// We anchor on "/iron-proxy/" and the ".yaml" suffix; the id is the
// basename component between them. Config paths under "Application
// Support" (with a space) can't be recovered from ps output because
// argv isn't quoted — but we don't need them, because the runtime dir
// is deterministic and IronProxyConfigPath rebuilds the path from the
// project id.
func parseIronProxyProjectID(command string) (string, bool) {
	const marker = "/iron-proxy/"
	idx := strings.LastIndex(command, marker)
	if idx < 0 {
		return "", false
	}
	rest := command[idx+len(marker):]
	yamlIdx := strings.Index(rest, ".yaml")
	if yamlIdx <= 0 {
		return "", false
	}
	projectID := rest[:yamlIdx]
	if strings.ContainsAny(projectID, " /\t") {
		return "", false
	}
	return projectID, true
}

// loadIronProxyInfoFromConfig reads the YAML iron-proxy was launched
// with and pulls back HTTPPort + HTTPSPort + TunnelPort + DNSPort.
func loadIronProxyInfoFromConfig(path string) (projectInfo, error) {
	blob, err := os.ReadFile(path)
	if err != nil {
		return projectInfo{}, fmt.Errorf("read %s: %w", path, err)
	}
	var raw struct {
		DNS struct {
			Listen string `yaml:"listen"`
		} `yaml:"dns"`
		Proxy struct {
			HTTPListen   string `yaml:"http_listen"`
			HTTPSListen  string `yaml:"https_listen"`
			TunnelListen string `yaml:"tunnel_listen"`
		} `yaml:"proxy"`
	}
	if err := yaml.Unmarshal(blob, &raw); err != nil {
		return projectInfo{}, fmt.Errorf("parse %s: %w", path, err)
	}
	_, httpPort, err := splitHostPortInt(raw.Proxy.HTTPListen)
	if err != nil {
		return projectInfo{}, fmt.Errorf("proxy.http_listen: %w", err)
	}
	_, httpsPort, err := splitHostPortInt(raw.Proxy.HTTPSListen)
	if err != nil {
		return projectInfo{}, fmt.Errorf("proxy.https_listen: %w", err)
	}
	_, tunnelPort, err := splitHostPortInt(raw.Proxy.TunnelListen)
	if err != nil {
		return projectInfo{}, fmt.Errorf("proxy.tunnel_listen: %w", err)
	}
	_, dnsPort, err := splitHostPortInt(raw.DNS.Listen)
	if err != nil {
		return projectInfo{}, fmt.Errorf("dns.listen: %w", err)
	}
	return projectInfo{
		HTTPPort:   httpPort,
		HTTPSPort:  httpsPort,
		TunnelPort: tunnelPort,
		DNSPort:    dnsPort,
	}, nil
}

func splitHostPortInt(hp string) (string, int, error) {
	host, portStr, err := net.SplitHostPort(hp)
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, fmt.Errorf("port not int: %w", err)
	}
	return host, port, nil
}
