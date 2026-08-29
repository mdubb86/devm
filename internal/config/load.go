package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/mdubb86/devm/internal/schema"
	"gopkg.in/yaml.v3"
)

// ErrNoConfig means dir has no devm.yaml at all. Callers that treat
// "not in a project" as a benign mode (e.g. `devm status` daemon-only
// output) must match on this sentinel — any other Load error means a
// devm.yaml exists but is unreadable or invalid, and must be surfaced,
// not swallowed as "no project here".
var ErrNoConfig = errors.New("no devm.yaml")

// strictDecode runs yaml.v3 with KnownFields(true) so ANY unknown key
// — top-level, nested, or deeply nested — hard-fails with a yaml-native
// error. Complements the friendlier CheckUnknownKeys pass above (which
// fires first and covers the common top-level + project + network typos
// with nicer messages), and closes the gap that pass leaves for nested
// blocks (services.<name>.*, base_image.*, etc.).
func strictDecode(data []byte, into any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(into); err != nil {
		return err
	}
	return nil
}

// Load reads devm.yaml (required) and devm.me.yaml (optional) from dir,
// validates each, deep-merges, and validates the result. Returns the
// merged validated Config.
func Load(dir string) (schema.Config, error) {
	basePath := filepath.Join(dir, "devm.yaml")
	baseBytes, err := os.ReadFile(basePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return schema.Config{}, fmt.Errorf("%w in %s", ErrNoConfig, dir)
		}
		return schema.Config{}, fmt.Errorf("read %s: %w", basePath, err)
	}
	if err := schema.CheckUnknownKeys(baseBytes); err != nil {
		return schema.Config{}, fmt.Errorf("%s: %w", basePath, err)
	}
	var base schema.Config
	if err := strictDecode(baseBytes, &base); err != nil {
		return schema.Config{}, fmt.Errorf("parse %s: %w", basePath, err)
	}
	if err := base.Validate(); err != nil {
		return schema.Config{}, fmt.Errorf("%s: %w", basePath, err)
	}

	overridePath := filepath.Join(dir, "devm.me.yaml")
	var override schema.ConfigOverride
	if _, err := os.Stat(overridePath); err == nil {
		ovBytes, err := os.ReadFile(overridePath)
		if err != nil {
			return schema.Config{}, fmt.Errorf("read %s: %w", overridePath, err)
		}
		if err := schema.CheckUnknownKeys(ovBytes); err != nil {
			return schema.Config{}, fmt.Errorf("%s: %w", overridePath, err)
		}
		if err := strictDecode(ovBytes, &override); err != nil {
			return schema.Config{}, fmt.Errorf("parse %s: %w", overridePath, err)
		}
	}

	merged, err := Merge(base, override)
	if err != nil {
		return schema.Config{}, fmt.Errorf("merge: %w", err)
	}
	// Validate against the project root so path-relative config
	// (volumes, labels) can be checked against the host filesystem.
	if err := merged.ValidateWithRoot(dir); err != nil {
		return schema.Config{}, fmt.Errorf("merged config: %w", err)
	}
	// ResolveEnv: expand $WORKSPACE in cfg.Env + services[*].env values,
	// reject reserved keys and unknown $VAR references, inject WORKSPACE
	// + IS_SANDBOX. All downstream consumers (templates, EnvArgs,
	// PersistentEnv, kit-env render) read the resolved cfg.Env.
	if err := schema.ResolveEnv(&merged, dir); err != nil {
		return schema.Config{}, fmt.Errorf("resolve env: %w", err)
	}
	return merged, nil
}
