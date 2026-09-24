package render

import (
	"encoding/json"
	"path/filepath"

	"github.com/mdubb86/devm/internal/schema"
)

// commandsManifest is the JSON shape written to /opt/devm/commands.json.
// The guest-side `run` binary reads it; the shape is a stable contract
// with cmd/run/main.go.
type commandsManifest struct {
	Repos map[string]manifestRepo `json:"repos"`
}

type manifestRepo struct {
	GuestPath string   `json:"guestPath"`
	Commands  []string `json:"commands"` // function names allowed via `run <name>`
}

// RenderCommandsManifest emits the deterministic JSON body of
// /opt/devm/commands.json. Always emits a valid JSON body — an empty
// repos map when cfg has no repos — so callers ship the file
// unconditionally. Every declared repo appears in the manifest even
// with zero commands (commands: []): the guest's `run` dispatcher uses
// a repo's presence in this map to distinguish "cwd is outside any
// devm repo" from "cwd is in a repo that just has no such command."
func RenderCommandsManifest(cfg schema.Config, macCwd string) ([]byte, error) {
	out := commandsManifest{Repos: map[string]manifestRepo{}}
	for repoName, r := range cfg.Repos {
		commands := make([]string, len(r.Commands))
		copy(commands, r.Commands)
		out.Repos[repoName] = manifestRepo{
			GuestPath: filepath.Join(schema.GuestHomeDir, r.ResolveLabel(macCwd)),
			Commands:  commands,
		}
	}
	// encoding/json sorts map keys lexically → byte-identical across runs.
	return json.Marshal(out)
}
