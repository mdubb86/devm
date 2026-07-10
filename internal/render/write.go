package render

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mdubb86/devm/internal/schema"
)

// WriteTemplateInstallers writes (or re-writes) the per-template installer
// scripts under .devm/templates/ and prunes stale ones.
//
// ApplyLive no longer calls this directly (it pipes a devmbundle into the
// guest instead, so the workspace stays clean of .devm/) — but
// ComputeTemplateChanges still reads repoRoot/.devm/templates/ as the
// on-disk "last applied" baseline for template diffing, and this is what
// populates that baseline in tests. See docs/superpowers/ for the known
// gap: nothing in production writes this baseline anymore now that both
// cold-start and ApplyLive are bundle-only, so template-change detection
// needs a follow-up before it can be trusted across reconcile runs.
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
