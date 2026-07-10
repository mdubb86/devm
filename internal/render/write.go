package render

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mdubb86/devm/internal/schema"
)

// WriteTemplateInstallers writes (or re-writes) the per-template installer
// scripts under .devm/templates/ and prunes stale ones. Called by ApplyLive
// just before running the in-sandbox dispatcher so that the sandbox always
// executes the latest rendered scripts.
func WriteTemplateInstallers(cfg schema.Config, repoRoot string) error {
	return writeTemplateInstallers(cfg, repoRoot)
}

func writeTemplateInstallers(cfg schema.Config, repoRoot string) error {
	templatesDir := filepath.Join(repoRoot, ".devm", "templates")
	if err := os.MkdirAll(templatesDir, 0o755); err != nil {
		return fmt.Errorf("mkdir .devm/templates: %w", err)
	}

	// Write per-template installers.
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

	// Prune stale installers — anything in .devm/templates/*.sh that
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
