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
// (/vm/apply-egress-enforcement) keep working across a daemon restart.
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
func AdoptIronProxies(ctx context.Context, sup *supervisor.Supervisor) error {
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
	}
	return nil
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
