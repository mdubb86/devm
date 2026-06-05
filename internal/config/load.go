package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mtwaage/devm/internal/schema"
	"gopkg.in/yaml.v3"
)

// Load reads devm.yaml (required) and devm.me.yaml (optional) from dir,
// validates each, deep-merges, and validates the result. Returns the
// merged validated Config.
func Load(dir string) (schema.Config, error) {
	basePath := filepath.Join(dir, "devm.yaml")
	baseBytes, err := os.ReadFile(basePath)
	if err != nil {
		return schema.Config{}, fmt.Errorf("read %s: %w", basePath, err)
	}
	var base schema.Config
	if err := yaml.Unmarshal(baseBytes, &base); err != nil {
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
		if err := yaml.Unmarshal(ovBytes, &override); err != nil {
			return schema.Config{}, fmt.Errorf("parse %s: %w", overridePath, err)
		}
	}

	merged, err := Merge(base, override)
	if err != nil {
		return schema.Config{}, fmt.Errorf("merge: %w", err)
	}
	// Validate against the project root so mounts[] entries can be
	// resolved and existence-checked against the host filesystem.
	if err := merged.ValidateWithRoot(dir); err != nil {
		return schema.Config{}, fmt.Errorf("merged config: %w", err)
	}
	return merged, nil
}
