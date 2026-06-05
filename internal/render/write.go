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
	if err := writeStaticFiles(cfg, repoRoot); err != nil {
		return err
	}
	return writeTemplateInstallers(cfg, repoRoot)
}

// WriteDevmDirStaticOnly regenerates the static parts of .devm/ (spec.yaml,
// Caddyfile, scripts/) without touching the per-template installer scripts
// under .devm/templates/. This preserves the on-disk installers as the
// "last-applied" snapshot so that a subsequent ComputeTemplateChanges call
// can still detect source-file changes that haven't been applied yet.
//
// Use this in the reconcile pre-diff step for running sandboxes. Use
// WriteDevmDir everywhere else (cold start, recreate, explicit re-render).
func WriteDevmDirStaticOnly(cfg schema.Config, repoRoot string) error {
	return writeStaticFiles(cfg, repoRoot)
}

// WriteTemplateInstallers writes (or re-writes) the per-template installer
// scripts under .devm/templates/ and prunes stale ones. Called by ApplyLive
// just before running the in-sandbox dispatcher so that the sandbox always
// executes the latest rendered scripts.
func WriteTemplateInstallers(cfg schema.Config, repoRoot string) error {
	return writeTemplateInstallers(cfg, repoRoot)
}

func writeStaticFiles(cfg schema.Config, repoRoot string) error {
	// Fail fast if the rendered spec.yaml isn't valid YAML. Beats
	// writing a broken file and discovering it only when sbx run tries
	// to load it (or the user runs `devm shell` and gets a cryptic
	// "did not find expected alphabetic or numeric character" from the
	// sbx kit loader).
	if err := LintRenderedSpec(cfg, repoRoot); err != nil {
		return err
	}
	devmDir := filepath.Join(repoRoot, ".devm")
	scriptsDir := filepath.Join(devmDir, "scripts")
	templatesDir := filepath.Join(devmDir, "templates")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		return fmt.Errorf("mkdir .devm/scripts: %w", err)
	}
	if err := os.MkdirAll(templatesDir, 0o755); err != nil {
		return fmt.Errorf("mkdir .devm/templates: %w", err)
	}

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
	return nil
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
