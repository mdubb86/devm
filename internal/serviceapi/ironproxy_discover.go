package serviceapi

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/mdubb86/devm/internal/ironproxy"
	"github.com/mdubb86/devm/internal/supervisor"
)

// DiscoverIronProxies returns project-id → pid for every running
// iron-proxy process whose binary path matches the one this daemon
// would launch.
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
func DiscoverIronProxies(ctx context.Context) (map[string]int, error) {
	binary, err := ironproxy.Path()
	if err != nil {
		return nil, fmt.Errorf("locate iron-proxy: %w", err)
	}
	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	return parseIronProxyProcesses(string(out), binary), nil
}

// AdoptIronProxies discovers running iron-proxies and registers each
// with the supervisor as adopted. Best-effort: errors are returned so
// the caller decides whether they're fatal, but in practice the daemon
// keeps starting on failure.
func AdoptIronProxies(ctx context.Context, sup *supervisor.Supervisor) error {
	procs, err := DiscoverIronProxies(ctx)
	if err != nil {
		return err
	}
	for projectID, pid := range procs {
		sup.Adopt(supervisor.Key{ProjectID: projectID, Role: supervisor.RoleProxy}, pid)
	}
	return nil
}

// parseIronProxyProcesses extracts iron-proxy entries from `ps -axo
// pid=,command=` output. Split out from DiscoverIronProxies so tests
// don't have to shell out.
func parseIronProxyProcesses(psOutput, ironProxyBinary string) map[string]int {
	out := map[string]int{}
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
		projectID, ok := parseConfigProjectID(command)
		if !ok {
			continue
		}
		out[projectID] = pid
	}
	return out
}

// parseConfigProjectID pulls the project id out of a command line like
// "/path/to/iron-proxy -config <runtime_dir>/iron-proxy/<project>.yaml".
// Paths-with-spaces (e.g., "Application Support") are handled because
// project ids can't contain spaces or slashes — basename component
// only — so we anchor on "/iron-proxy/" and the ".yaml" suffix.
func parseConfigProjectID(command string) (string, bool) {
	idx := strings.LastIndex(command, "/iron-proxy/")
	if idx < 0 {
		return "", false
	}
	rest := command[idx+len("/iron-proxy/"):]
	yamlIdx := strings.Index(rest, ".yaml")
	if yamlIdx <= 0 {
		return "", false
	}
	id := rest[:yamlIdx]
	if strings.ContainsAny(id, " /\t") {
		return "", false
	}
	return id, true
}
