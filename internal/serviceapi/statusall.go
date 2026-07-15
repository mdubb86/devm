package serviceapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/mdubb86/devm/internal/supervisor"
)

// ProjectStatus is one row of GET /status/all — a cross-project
// summary combining VM running state (from tart) with iron-proxy
// health (from the supervisor) for every project the daemon has a
// persisted StateSnapshot for. Backs `devm status --all`.
type ProjectStatus struct {
	ProjectID string      `json:"project_id"`
	VMName    string      `json:"vm_name"`
	VMRunning bool        `json:"vm_running"`
	Proxy     ProxyHealth `json:"proxy"`
}

// RegisterStatusAllHandler wires GET /status/all. sup is queried for
// each project's iron-proxy health; tr supplies the running-VM set.
// Read-only report — unlike healNoSecretProxiesAtStartup (removed
// startheal.go), this never spawns anything.
func RegisterStatusAllHandler(s *Server, sup *supervisor.Supervisor, tr TartLister) {
	s.Register("/status/all", func(w http.ResponseWriter, r *http.Request) {
		out, err := listProjectStatuses(r.Context(), sup, tr)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
}

// listProjectStatuses enumerates every persisted StateSnapshot in
// StateDir(), joins each against the running-VM set from tr.List, and
// computes iron-proxy health per project. Mirrors the StateDir ×
// running-VM join that healNoSecretProxiesAtStartup (startheal.go,
// removed in 4e69ab9) used to decide what to respawn — this is the
// read-only report version of that same enumeration.
func listProjectStatuses(ctx context.Context, sup *supervisor.Supervisor, tr TartLister) ([]ProjectStatus, error) {
	vms, err := tr.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("tart list: %w", err)
	}
	running := make(map[string]bool, len(vms))
	for _, vm := range vms {
		if vm.Running {
			running[vm.Name] = true
		}
	}

	entries, err := os.ReadDir(StateDir())
	if err != nil {
		if os.IsNotExist(err) {
			return []ProjectStatus{}, nil
		}
		return nil, fmt.Errorf("read state dir: %w", err)
	}

	out := make([]ProjectStatus, 0, len(entries))
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
		out = append(out, ProjectStatus{
			ProjectID: projectID,
			VMName:    snap.Cfg.Project.VMName,
			VMRunning: running[snap.Cfg.Project.VMName],
			Proxy:     computeProxyHealth(sup, projectID),
		})
	}
	return out, nil
}
