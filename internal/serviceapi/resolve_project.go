// resolve_project.go — POST /vm/resolve-project maps a cwd to a
// project name + state-dir path. CLI verbs call this at entry to
// find where devm.yaml lives.
package serviceapi

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/mdubb86/devm/internal/identity"
)

type resolveProjectResponse struct {
	Name     string `json:"name"`
	StateDir string `json:"state_dir"`
	Cwd      string `json:"cwd"`
}

func handleResolveProject(cfg identity.Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "resolve-project: POST only", http.StatusMethodNotAllowed)
			return
		}
		cwd := r.URL.Query().Get("cwd")
		if cwd == "" {
			http.Error(w, "resolve-project: cwd query param required", http.StatusBadRequest)
			return
		}
		name, matchedCwd, ok, err := FindProjectByCwd(cfg, cwd)
		if err != nil {
			http.Error(w, fmt.Sprintf("resolve-project: scan: %v", err), http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, fmt.Sprintf("resolve-project: %s is not a devm project; run 'devm init <name>' to register it", cwd), http.StatusNotFound)
			return
		}
		resp := resolveProjectResponse{
			Name:     name,
			StateDir: stateDirForProject(cfg, name),
			Cwd:      matchedCwd,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
}
