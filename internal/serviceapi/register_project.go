// register_project.go — POST /vm/register-project seeds a new
// project's devm.yaml in its state dir and registers the calling
// cwd, so `devm init <name>` can turn an unregistered directory into
// a devm project that /vm/resolve-project will find on later calls.
package serviceapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/mdubb86/devm/internal/identity"
)

type registerProjectResponse struct {
	Name       string `json:"name"`
	ConfigPath string `json:"config_path"`
}

func handleRegisterProject(cfg identity.Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "register-project: POST only", http.StatusMethodNotAllowed)
			return
		}
		name := r.URL.Query().Get("name")
		cwd := r.URL.Query().Get("cwd")
		if name == "" || cwd == "" {
			http.Error(w, "register-project: name and cwd query params required", http.StatusBadRequest)
			return
		}
		// Reject if cwd is already registered under a different project.
		if existing, _, ok, err := FindProjectByCwd(cfg, cwd); err != nil {
			http.Error(w, fmt.Sprintf("register-project: %v", err), http.StatusInternalServerError)
			return
		} else if ok && existing != name {
			http.Error(w, fmt.Sprintf("register-project: %s is already registered as project %q", cwd, existing), http.StatusConflict)
			return
		}
		// Reject if the project already has a devm.yaml (would clobber).
		configPath := filepath.Join(stateDirForProject(cfg, name), "devm.yaml")
		if _, err := os.Stat(configPath); err == nil {
			http.Error(w, fmt.Sprintf("register-project: project %q already exists at %s", name, stateDirForProject(cfg, name)), http.StatusConflict)
			return
		}
		// Seed the file.
		seed := "# devm.yaml — see project schema docs\nproject:\n  name: " + name + "\n"
		if err := os.MkdirAll(stateDirForProject(cfg, name), 0o755); err != nil {
			http.Error(w, fmt.Sprintf("register-project: mkdir: %v", err), http.StatusInternalServerError)
			return
		}
		if err := os.WriteFile(configPath, []byte(seed), 0o644); err != nil {
			http.Error(w, fmt.Sprintf("register-project: write seed: %v", err), http.StatusInternalServerError)
			return
		}
		if err := AddCwdAlias(cfg, name, cwd); err != nil {
			http.Error(w, fmt.Sprintf("register-project: add alias: %v", err), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(registerProjectResponse{
			Name:       name,
			ConfigPath: configPath,
		})
	})
}
