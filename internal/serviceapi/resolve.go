package serviceapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/mdubb86/devm/internal/identity"
)

// WorkspaceEntry is one row of GET /workspaces — a project's primary
// workspace repo, pairing the path as it appears inside the guest
// clone (GuestPath) with the Mac-side volume storage path
// (StoragePath). Used to translate a VM-emitted path (e.g. from a
// screenshot or test-output path a guest process printed) back to
// where it actually lives on the Mac.
type WorkspaceEntry struct {
	ProjectName string `json:"project"`
	GuestPath   string `json:"guest_path"`
	StoragePath string `json:"storage_path"`
}

// RegisterWorkspacesHandler wires GET /workspaces. Enumerates every
// persisted StateSnapshot in StateDir() — same source `devm status
// --all` reads — and reports one entry per project with a primary
// workspace repo in Cfg.Repos. Projects without one have no guest
// clone to resolve paths against, so they're skipped.
//
// TODO(Task 17): listWorkspaces is currently a no-op (see below) —
// the primary-repo lookup needs to walk Cfg.Repos.
func RegisterWorkspacesHandler(s *Server, cfg identity.Config) {
	s.Register("/workspaces", func(w http.ResponseWriter, r *http.Request) {
		out, err := listWorkspaces(cfg)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
}

// listWorkspaces mirrors listProjectStatuses' StateDir enumeration.
func listWorkspaces(cfg identity.Config) ([]WorkspaceEntry, error) {
	entries, err := os.ReadDir(StateDir(cfg))
	if err != nil {
		if os.IsNotExist(err) {
			return []WorkspaceEntry{}, nil
		}
		return nil, fmt.Errorf("read state dir: %w", err)
	}

	out := make([]WorkspaceEntry, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		projectID := strings.TrimSuffix(name, ".json")

		snap, err := ReadStateSnapshot(cfg, projectID)
		if err != nil || snap == nil {
			continue
		}
		// TODO(Task 24): resolve the entry in snap.Cfg.Repos with
		// Primary == true and append a WorkspaceEntry for it (guest path
		// + StoragePath via mirrorMacDir). No-op (skip every project)
		// until then.
	}
	return out, nil
}
