package orchestrator

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/mdubb86/devm/internal/serviceapi/sshconfig"
	"github.com/mdubb86/devm/internal/softnet"
)

// IngressConfigClient is the subset of *serviceapi.Client EmitSSHConfig
// needs: the project's daemon-allocated SSH host port. Embedded into
// VMAdminClient and StopVMClient so both callers' real client
// satisfies it without a separate wiring seam.
type IngressConfigClient interface {
	IngressConfig(ctx context.Context, name string) (serviceapi.VMIngressConfigResponse, error)
}

// EmitSSHConfig walks the daemon's state dir and asks tart which VMs
// are currently running, then emits an ssh_config include with one
// Host block per running project. Called from lifecycle hooks
// (cold-start, warm-attach, stop) so `ssh devm-<vm-name>` reflects
// the currently running set.
//
// Under softnet the guest IP isn't Mac-routable, so each Host block
// points at 127.0.0.1 and the project's daemon-allocated SSH host
// port (fetched via GET /vm/ingress-config) instead of a `tart ip`
// lookup.
//
// Errors are wrapped for logging by the caller; caller must decide
// whether to fail loud or log-and-continue. In practice callers log
// and continue — a stale ssh_config file doesn't block VM operation.
func EmitSSHConfig(ctx context.Context, tr *tart.Tart, client IngressConfigClient) error {
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
		ingress, err := client.IngressConfig(ctx, name)
		if err != nil || ingress.SSHHostPort == 0 {
			// No SSH host port yet (VM mid-cold-start, or a transient
			// daemon-restart window before recoverProjectState reloads
			// it — see StateSnapshot.SSHHostPort). Skip, exactly as the
			// prior `tart ip` lookup skipped a VM with no IP yet.
			continue
		}
		out = append(out, sshconfig.Entry{
			Name: name,
			Host: softnet.HostLoopIP,
			Port: ingress.SSHHostPort,
		})
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
