package render

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mtwaage/devm/internal/schema"
	"github.com/mtwaage/devm/internal/scripts"
)

// WriteDevmDir regenerates the .devm/ cache in repoRoot with current
// config values. Always overwrites — .devm/ is CLI-owned.
//
// As of the templates feature, this also writes the install-templates
// dispatcher and per-template installer scripts. The templates dir is
// pruned of any installer that the current render set doesn't produce
// (otherwise removing a template from devm.yaml would leave its
// installer behind and the dispatcher would keep running it).
func WriteDevmDir(cfg schema.Config, repoRoot string) error {
	devmDir := filepath.Join(repoRoot, ".devm")
	scriptsDir := filepath.Join(devmDir, "scripts")
	templatesDir := filepath.Join(devmDir, "templates")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		return fmt.Errorf("mkdir .devm/scripts: %w", err)
	}
	if err := os.MkdirAll(templatesDir, 0o755); err != nil {
		return fmt.Errorf("mkdir .devm/templates: %w", err)
	}

	// 1. Static files (always emitted).
	staticFiles := map[string]string{
		filepath.Join(devmDir, "Caddyfile"):               Caddyfile(cfg),
		filepath.Join(devmDir, "spec.yaml"):               SpecYAML(cfg, repoRoot),
		filepath.Join(scriptsDir, "init-volumes.sh"):      scripts.InitVolumes,
		filepath.Join(scriptsDir, "devm-exec.sh"):         scripts.DevmExec,
		filepath.Join(scriptsDir, "install-templates.sh"): scripts.InstallTemplates,
	}
	for path, content := range staticFiles {
		if err := writeFile(path, content); err != nil {
			return err
		}
	}

	// 2. Per-template installers.
	installers, err := RenderTemplates(cfg, repoRoot)
	if err != nil {
		return fmt.Errorf("render templates: %w", err)
	}
	keep := make(map[string]struct{}, len(installers))
	for path, content := range installers {
		if err := writeFile(path, content); err != nil {
			return err
		}
		keep[filepath.Base(path)] = struct{}{}
	}

	// 3. Prune stale installers — anything in .devm/templates/*.sh that
	// the current render set didn't produce.
	entries, err := os.ReadDir(templatesDir)
	if err != nil {
		return fmt.Errorf("readdir .devm/templates: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".sh" {
			continue
		}
		if _, ok := keep[e.Name()]; ok {
			continue
		}
		if err := os.Remove(filepath.Join(templatesDir, e.Name())); err != nil {
			return fmt.Errorf("remove stale template %s: %w", e.Name(), err)
		}
	}
	return nil
}

func writeFile(path, content string) error {
	mode := os.FileMode(0o644)
	if filepath.Ext(path) == ".sh" {
		mode = 0o755
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
