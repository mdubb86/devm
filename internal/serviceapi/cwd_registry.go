// cwd_registry.go — per-project cwd → project mapping. Written by
// `devm init` (and `/vm/start`); read by `POST /vm/resolve-project`.
package serviceapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mdubb86/devm/internal/identity"
)

const cwdRegistryFilename = "cwds.json"

func stateDirForProject(cfg identity.Config, name string) string {
	return filepath.Join(cfg.RuntimeDir(), name)
}

func cwdRegistryPath(cfg identity.Config, name string) string {
	return filepath.Join(stateDirForProject(cfg, name), cwdRegistryFilename)
}

// AddCwdAlias appends cwd to the project's registered-cwd list. No-op
// if cwd is already present. Creates the state directory if needed.
func AddCwdAlias(cfg identity.Config, name, cwd string) error {
	existing, err := ReadCwdAliases(cfg, name)
	if err != nil {
		return err
	}
	for _, e := range existing {
		if e == cwd {
			return nil
		}
	}
	updated := append(existing, cwd)
	body, err := json.MarshalIndent(updated, "", "  ")
	if err != nil {
		return fmt.Errorf("cwd-registry marshal: %w", err)
	}
	if err := os.MkdirAll(stateDirForProject(cfg, name), 0o755); err != nil {
		return fmt.Errorf("cwd-registry mkdir: %w", err)
	}
	tmp := cwdRegistryPath(cfg, name) + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return fmt.Errorf("cwd-registry write tmp: %w", err)
	}
	if err := os.Rename(tmp, cwdRegistryPath(cfg, name)); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("cwd-registry rename: %w", err)
	}
	return nil
}

// ReadCwdAliases returns the project's registered cwds. (nil, nil)
// when the registry file doesn't exist.
func ReadCwdAliases(cfg identity.Config, name string) ([]string, error) {
	body, err := os.ReadFile(cwdRegistryPath(cfg, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cwd-registry read: %w", err)
	}
	var out []string
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("cwd-registry unmarshal: %w", err)
	}
	return out, nil
}

// FindProjectByCwd walks every <RuntimeDir>/*/cwds.json looking for
// cwd or an ancestor of cwd. Returns the matching project name and the
// matched ancestor cwd, or ("", "", false, nil) when none matches.
// Walks cwd up its path components until an exact match is found or
// the root is reached.
func FindProjectByCwd(cfg identity.Config, cwd string) (name, matchedCwd string, ok bool, err error) {
	entries, err := os.ReadDir(cfg.RuntimeDir())
	if errors.Is(err, os.ErrNotExist) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, fmt.Errorf("cwd-registry scan: %w", err)
	}
	registered := map[string]string{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		aliases, err := ReadCwdAliases(cfg, e.Name())
		if err != nil {
			return "", "", false, err
		}
		for _, a := range aliases {
			registered[a] = e.Name()
		}
	}
	for p := cwd; ; {
		if name, ok := registered[p]; ok {
			return name, p, true, nil
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", "", false, nil
		}
		p = parent
	}
}
