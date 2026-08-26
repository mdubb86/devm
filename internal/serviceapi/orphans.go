package serviceapi

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/mdubb86/devm/internal/daemonlog"
	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/serviceapi/sshkeys"
)

// detectOrphanVMs returns, sorted, the names of running tart VMs that
// are provably devm's — carrying sidecar artifacts devm itself wrote
// (an iron-proxy config or a per-project ssh dir) — but that have no
// state snapshot, meaning the daemon has lost track of them. Daemon
// state and running VMs have independent lifetimes (VMs survive
// uninstall/reinstall and state-dir loss), and an orphan's softnet
// squats its old pool IP binds invisibly. VMs without devm sidecars
// (base images, hand-made tart VMs) are never reported: absent
// evidence, they're not devm's to comment on.
func detectOrphanVMs(ctx context.Context, cfg identity.Config, tr TartLister) ([]string, error) {
	vms, err := tr.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("tart list: %w", err)
	}
	var orphans []string
	for _, vm := range vms {
		if !vm.Running {
			continue
		}
		// VM names come from tart, not from devm's validated schema —
		// refuse anything that couldn't be a devm project id before
		// using it in path joins.
		if vm.Name == "" || strings.ContainsAny(vm.Name, "/\\") || strings.Contains(vm.Name, "..") {
			continue
		}
		if snap, err := ReadStateSnapshot(cfg, vm.Name); err == nil && snap != nil {
			continue // tracked
		}
		if !hasDevmSidecars(cfg, vm.Name) {
			continue // not provably devm's
		}
		orphans = append(orphans, vm.Name)
	}
	sort.Strings(orphans)
	return orphans, nil
}

// hasDevmSidecars reports whether devm-authored per-project artifacts
// exist for name: an iron-proxy config or an ssh project dir.
func hasDevmSidecars(cfg identity.Config, name string) bool {
	if path, err := IronProxyConfigPath(cfg, name); err == nil {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	if _, err := os.Stat(sshkeys.ProjectDir(cfg, name)); err == nil {
		return true
	}
	return false
}

// reportOrphanVMs logs each detected orphan with remediation guidance.
// Called on the daemon-startup path (best-effort, in a goroutine —
// tart list can take seconds and must not stall startup).
func reportOrphanVMs(ctx context.Context, cfg identity.Config, tr TartLister) {
	orphans, err := detectOrphanVMs(ctx, cfg, tr)
	if err != nil {
		daemonlog.Errorf("serviceapi: orphan VM scan: %v", err)
		return
	}
	for _, name := range orphans {
		daemonlog.Errorf("serviceapi: VM %q is running with devm artifacts but no daemon state — its softnet may be squatting pool IP binds; `devm stop` it from its workspace (or `devm teardown`) to recover", name)
	}
}
