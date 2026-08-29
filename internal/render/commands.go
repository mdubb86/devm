package render

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/mdubb86/devm/internal/schema"
)

// commandsManifest is the JSON shape written to /opt/devm/commands.json.
// The guest-side `run` binary reads it; the shape is a stable contract
// with cmd/run/main.go.
type commandsManifest struct {
	Repos map[string]manifestRepo `json:"repos"`
}

type manifestRepo struct {
	GuestPath string                     `json:"guestPath"`
	Commands  map[string]manifestCommand `json:"commands"`
}

type manifestCommand struct {
	Exec    string `json:"exec"`
	Startup bool   `json:"startup"`
}

// RenderCommandsManifest emits the deterministic JSON body of
// /opt/devm/commands.json. All `>NAME` script refs are resolved at
// render time so the guest binary never touches the scripts library.
// Always emits a valid JSON body — an empty repos map when cfg has no
// commands — so callers ship the file unconditionally.
func RenderCommandsManifest(cfg schema.Config, macCwd string) ([]byte, error) {
	out := commandsManifest{Repos: map[string]manifestRepo{}}
	for repoName, r := range cfg.Repos {
		if len(r.Commands) == 0 {
			continue
		}
		repo := manifestRepo{
			GuestPath: filepath.Join(schema.GuestHomeDir, r.ResolveLabel(macCwd)),
			Commands:  make(map[string]manifestCommand, len(r.Commands)),
		}
		for cmdName, c := range r.Commands {
			body := c.Exec
			if refName, ok := schema.ParseScriptRef(c.Exec); ok {
				body = strings.Join(cfg.Scripts[refName], " && ")
			}
			repo.Commands[cmdName] = manifestCommand{
				Exec:    body,
				Startup: c.StartupBool(),
			}
		}
		out.Repos[repoName] = repo
	}
	// encoding/json sorts map keys lexically → byte-identical across runs.
	return json.Marshal(out)
}
