package serviceapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/schema"
)

// WorkspaceEntry is one row of GET /workspaces — a mirrored repo or
// volume, pairing its guest-view path (GuestPath) with its Mac-side
// mirror storage path (StoragePath) and the label its mutagen sync
// session runs under. Used to translate a VM-emitted path (e.g. from
// a screenshot or test-output path a guest process printed) back to
// where it actually lives on the Mac.
type WorkspaceEntry struct {
	ProjectName string `json:"project"`
	Label       string `json:"label"`
	GuestPath   string `json:"guest_path"`
	StoragePath string `json:"storage_path"`
}

// RegisterWorkspacesHandler wires GET /workspaces. Enumerates every
// persisted StateSnapshot in StateDir() — same source `devm status
// --all` reads — and reports one entry per mirrored repo or volume:
// the primary repo (mirrored by default), every secondary repo that
// opts in with `volume: true`, and every volumes.<name> entry.
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

// listWorkspaces mirrors listProjectStatuses' StateDir enumeration,
// then expands each project's persisted Cfg into one WorkspaceEntry
// per mirrored repo/volume.
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
		out = append(out, projectWorkspaceEntries(cfg, projectID, &snap.Cfg)...)
	}
	return out, nil
}

// projectWorkspaceEntries builds one WorkspaceEntry per mirrored repo
// or volume declared in pcfg, for the given projectID. Label
// resolution mirrors BuildEntities' resolveRepoLabel/
// resolveVolumeLabel rules, with one deliberate divergence: a
// URL-nil, label-nil primary repo would normally fall back to the
// basename of the Mac checkout dir (macCwd) — that value isn't
// persisted on StateSnapshot, so this falls back to projectID
// instead, which is the same string under the default
// `project.name` == checkout-dirname convention.
func projectWorkspaceEntries(cfg identity.Config, projectID string, pcfg *schema.Config) []WorkspaceEntry {
	var out []WorkspaceEntry

	primaryName := findPrimaryRepoName(pcfg)

	repoNames := make([]string, 0, len(pcfg.Repos))
	for n := range pcfg.Repos {
		repoNames = append(repoNames, n)
	}
	sort.Strings(repoNames)

	for _, name := range repoNames {
		r := pcfg.Repos[name]
		isPrimary := name == primaryName

		var included bool
		if isPrimary {
			included = r.Volume == nil || *r.Volume
		} else {
			included = r.Volume != nil && *r.Volume
		}
		if !included {
			continue
		}

		label := resolveRepoLabel(r, projectID)
		out = append(out, WorkspaceEntry{
			ProjectName: projectID,
			Label:       label,
			GuestPath:   filepath.Join(guestHomeDir, label),
			StoragePath: mirrorMacDir(cfg, projectID, label),
		})
	}

	volNames := make([]string, 0, len(pcfg.Volumes))
	for n := range pcfg.Volumes {
		volNames = append(volNames, n)
	}
	sort.Strings(volNames)

	for _, name := range volNames {
		v := pcfg.Volumes[name]
		label := resolveVolumeLabel(v)
		out = append(out, WorkspaceEntry{
			ProjectName: projectID,
			Label:       label,
			GuestPath:   v.Path,
			StoragePath: mirrorMacDir(cfg, projectID, label),
		})
	}

	return out
}
