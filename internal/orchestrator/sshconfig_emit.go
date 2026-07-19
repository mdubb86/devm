package orchestrator

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/mdubb86/devm/internal/serviceapi/sshconfig"
)

// EmitSSHConfig walks the daemon's state dir and asks tart which VMs
// are currently running, then emits an ssh_config include with one
// Host block per running project. Called from lifecycle hooks
// (cold-start, warm-attach, stop) so `ssh devm-<vm-name>` reflects
// the currently running set.
//
// Softnet binds each project's guest :22 on its allocated ProjectIP
// (per-project bind isolation) and DNS answers <project>.test ->
// ProjectIP, so the Host block just needs the project name — no
// daemon round trip to resolve a host port or loopback address.
//
// In fallback mode (no portbinder helper — see internal/serviceapi's
// helperAvailable), the persisted snapshot's PickedSSHPort is non-zero
// and sshconfig.Entry carries it through as PickedPort, which flips the
// emitted block to `HostName 127.0.0.1` + `Port <picked>` instead.
//
// Errors are wrapped for logging by the caller; caller must decide
// whether to fail loud or log-and-continue. In practice callers log
// and continue — a stale ssh_config file doesn't block VM operation.
func EmitSSHConfig(ctx context.Context, tr *tart.Tart) error {
	vms, err := tr.List(ctx)
	if err != nil {
		return fmt.Errorf("tart list: %w", err)
	}
	running := make(map[string]bool, len(vms))
	for _, v := range vms {
		if v.Running {
			running[v.Name] = true
		}
	}
	projectIDs, err := listStateProjects()
	if err != nil {
		return fmt.Errorf("list state projects: %w", err)
	}
	var out []sshconfig.Entry
	for _, id := range projectIDs {
		snap, err := serviceapi.ReadStateSnapshot(id)
		if err != nil || snap == nil {
			continue
		}
		name := snap.Cfg.Project.Name
		if !running[name] {
			continue
		}
		out = append(out, sshconfig.Entry{Name: name, PickedPort: snap.PickedSSHPort})
	}
	return sshconfig.Emit(out)
}

// listStateProjects lists project IDs devm has state snapshots for.
func listStateProjects() ([]string, error) {
	entries, err := os.ReadDir(serviceapi.StateDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		out = append(out, strings.TrimSuffix(name, ".json"))
	}
	return out, nil
}
