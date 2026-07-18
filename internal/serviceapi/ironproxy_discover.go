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
// Spawned iron-proxies are intentionally setsid'd (see
// internal/supervisor/setsid_darwin.go) so they survive daemon
// death. Without re-attachment, a daemon restart would leave them
// running but unmanaged — stop/status would say "not present" while
// the process kept enforcing egress. Discovery on startup closes
// that gap.
//
// Matching is intentionally strict: the command must start with the
// canonical iron-proxy binary path. We never adopt unrelated processes.
func DiscoverIronProxies(ctx context.Context) ([]DiscoveredIronProxy, error) {
	runDir, err := EnsureRuntimeDir()
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
// while the running VM's dnsmasq + nftables still point at the old
// ones — silent DNS failure inside the guest.
//
// Best-effort per-process: a project whose config file is missing,
// unreadable, or malformed is adopted (so /vm/stop still finds it) but
// left out of ironProxyState. The bump-in-the-log is via the returned
// error only if the discovery step itself failed; per-process
// rehydrate failures are swallowed silently to match the "best-effort"
// contract callers expect.
//
// Beyond rehydrating ironProxyState, each recovered project also gets
// its VM IP re-stashed and its direct routes rebuilt from the on-disk
// state snapshot (recoverProjectState) — both are in-memory-only and
// otherwise lost on daemon restart, breaking direct-service DNS for a
// VM that's still running under an orphaned iron-proxy.
func AdoptIronProxies(ctx context.Context, sup *supervisor.Supervisor, tr *tart.Tart, routes *Routes) error {
	procs, err := DiscoverIronProxies(ctx)
	if err != nil {
		return err
	}
	for _, p := range procs {
		sup.Adopt(supervisor.Key{ProjectID: p.ProjectID, Role: supervisor.RoleProxy}, p.PID)
		path, err := IronProxyConfigPath(p.ProjectID)
		if err != nil {
			continue
		}
		info, err := loadIronProxyInfoFromConfig(path)
		if err != nil {
			continue
		}
		ironProxyState.put(p.ProjectID, info)
		recoverProjectState(ctx, tr, routes, p.ProjectID)
	}
	return nil
}

// recoverProjectState rebuilds the parts of a recovered project's
// in-memory state that live outside ironProxyState's config-file
// rehydration: the stashed VM IP (read fresh via `tart ip`, since the
// config file iron-proxy was launched with doesn't record it), the
// Docker flag and SSH host port (both read back from the last-applied
// state snapshot, since neither is part of iron-proxy's own config
// shape), and the project's direct routes. It's split out of
// AdoptIronProxies's loop so it can be unit tested without shelling out
// to `ps` (DiscoverIronProxies).
//
// Both pieces are best-effort and independent: a VM that isn't
// running yet (tart ip fails) doesn't block rebuilding routes, and a
// missing/malformed snapshot (or a project with no direct services)
// doesn't block the VM-IP stash. There's simply nothing to recover
// for the piece that failed.
//
// Only Direct routes are rebuilt here. Proxied (non-direct) routes
// depend on the VM's IP as BackendHost and are normally re-pushed by
// the CLI (`devm shell` auto-apply, `devm reconcile`); rebuilding them
// here is out of scope for this recovery path — see buildRoutes in
// cmd/devm/route.go for how the CLI constructs the full set.
func recoverProjectState(ctx context.Context, tr *tart.Tart, routes *Routes, projectID string) {
	if ip, err := tr.IP(ctx, projectID); err == nil {
		info, _ := ironProxyState.get(projectID)
		info.VMIP = ip
		ironProxyState.put(projectID, info)
	}

	snap, err := ReadStateSnapshot(projectID)
	if err != nil || snap == nil {
		return
	}

	info, _ := ironProxyState.get(projectID)
	info.Docker = snap.Cfg.Docker
	// SSHHostPort isn't part of iron-proxy's on-disk YAML config (it's
	// not an iron-proxy concept), so loadIronProxyInfoFromConfig can't
	// recover it. Restore it from the state snapshot instead — the
	// orchestrator stamps the current value into every snapshot it
	// writes — so a daemon restart doesn't strand a running VM's SSH
	// port at 0 and force a re-allocation that would orphan any
	// ssh_config already emitted with the old port.
	info.SSHHostPort = snap.SSHHostPort
	ironProxyState.put(projectID, info)

	var directRoutes []Route
	for _, svc := range snap.Cfg.Services {
		if !svc.Direct || svc.Hostname == "" {
			continue
		}
		directRoutes = append(directRoutes, Route{
			Hostname:    svc.Hostname,
			BackendPort: svc.Port,
			Direct:      true,
			Project:     projectID,
		})
	}
	if len(directRoutes) > 0 {
		routes.Apply(projectID, directRoutes)
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
// with and pulls back MacHost + HTTPPort + HTTPSPort + DNSPort. The
// three listen strings are all "MacHost:port" (see IronProxyConfig.YAML
// in ironproxy.go) and MacHost is the same across all three because a
// single per-project iron-proxy binds all listeners on one vmnet
// bridge IP.
func loadIronProxyInfoFromConfig(path string) (ironProxyInfo, error) {
	blob, err := os.ReadFile(path)
	if err != nil {
		return ironProxyInfo{}, fmt.Errorf("read %s: %w", path, err)
	}
	var raw struct {
		DNS struct {
			Listen string `yaml:"listen"`
		} `yaml:"dns"`
		Proxy struct {
			HTTPListen  string `yaml:"http_listen"`
			HTTPSListen string `yaml:"https_listen"`
		} `yaml:"proxy"`
	}
	if err := yaml.Unmarshal(blob, &raw); err != nil {
		return ironProxyInfo{}, fmt.Errorf("parse %s: %w", path, err)
	}
	httpHost, httpPort, err := splitHostPortInt(raw.Proxy.HTTPListen)
	if err != nil {
		return ironProxyInfo{}, fmt.Errorf("proxy.http_listen: %w", err)
	}
	_, httpsPort, err := splitHostPortInt(raw.Proxy.HTTPSListen)
	if err != nil {
		return ironProxyInfo{}, fmt.Errorf("proxy.https_listen: %w", err)
	}
	_, dnsPort, err := splitHostPortInt(raw.DNS.Listen)
	if err != nil {
		return ironProxyInfo{}, fmt.Errorf("dns.listen: %w", err)
	}
	return ironProxyInfo{
		MacHost:   httpHost,
		HTTPPort:  httpPort,
		HTTPSPort: httpsPort,
		DNSPort:   dnsPort,
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
