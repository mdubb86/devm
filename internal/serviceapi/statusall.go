package serviceapi

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"

	"github.com/mdubb86/devm/internal/identity"
)

// ProjectStatus is one row of GET /status/all — a cross-project
// summary combining VM running state with iron-proxy health for every
// project the daemon has a StateCache row for. Backs `devm status
// --all`.
type ProjectStatus struct {
	Name      string      `json:"name"`
	VMRunning bool        `json:"vm_running"`
	Proxy     ProxyHealth `json:"proxy"`
	// Orphaned marks a running VM that carries devm sidecar artifacts
	// but no state snapshot — devm-created, daemon lost track of it
	// (see detectOrphanVMs). Such rows have no meaningful Proxy value.
	Orphaned bool `json:"orphaned,omitempty"`
}

// RegisterStatusAllHandler wires GET /status/all. Every project row
// comes from cache; tr supplies the running-VM set used only for
// orphan detection (see detectOrphanVMs), which stays a live tart
// query — an orphan is by definition a VM with no cache row. Purely a
// read-only report — it never spawns anything.
func RegisterStatusAllHandler(s *Server, cfg identity.Config, tr TartLister, cache *StateCache) {
	s.Register("/status/all", func(w http.ResponseWriter, r *http.Request) {
		out, err := projectStatusesFromCache(r.Context(), cfg, tr, cache)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
}

// projectStatusesFromCache builds one ProjectStatus per cache row,
// sorted by name for deterministic output, then appends live-detected
// orphan VMs.
func projectStatusesFromCache(ctx context.Context, cfg identity.Config, tr TartLister, cache *StateCache) ([]ProjectStatus, error) {
	rows := cache.AllProjectRows()
	out := make([]ProjectStatus, 0, len(rows))
	for name, row := range rows {
		out = append(out, ProjectStatus{
			Name:      name,
			VMRunning: row.VMState == VMRunning,
			Proxy:     row.IronProxyHealth,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	// Orphaned devm VMs — running, sidecar-evidenced, no cache row —
	// get their own rows so `devm status --all` surfaces them instead
	// of silently omitting a VM that's burning RAM and squatting pool
	// IP binds.
	orphans, err := detectOrphanVMs(ctx, cfg, tr)
	if err != nil {
		return nil, err
	}
	for _, name := range orphans {
		out = append(out, ProjectStatus{Name: name, VMRunning: true, Orphaned: true})
	}
	return out, nil
}
