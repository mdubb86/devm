package render

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/scripts"
)

// WriteDevmDir regenerates the .devm/ cache in repoRoot with current
// config values. Always overwrites — .devm/ is CLI-owned.
//
// Writes:
//   - .devm/.env (project env)
//   - .devm/templates/*.sh (per-template installers)
//   - .devm/scripts/with-devm-env (sources .env then execs argv; PATH
//     surfaces this as `with-devm-env` inside the VM, so users can
//     `with-devm-env <cmd>` themselves. Also invoked by
//     orchestrator/shell.go:attachShell to hand the interactive shell
//     the project env)
//   - .devm/scripts/install-templates.sh (invoked by the provisioner
//     at cold-start and by apply_live on template changes; loops over
//     .devm/templates/*.sh and runs each)
func WriteDevmDir(cfg schema.Config, repoRoot string) error {
	if err := WriteDevmEnv(cfg, repoRoot); err != nil {
		return err
	}
	if err := writeStaticScripts(repoRoot); err != nil {
		return err
	}
	return writeTemplateInstallers(cfg, repoRoot)
}

func writeStaticScripts(repoRoot string) error {
	dir := filepath.Join(repoRoot, ".devm", "scripts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir .devm/scripts: %w", err)
	}
	// No .sh suffix on with-devm-env: PATH-based invocation
	// (`with-devm-env <cmd>`) needs the bare name.
	staticFiles := map[string]string{
		"with-devm-env":         scripts.WithDevmEnv,
		"install-templates.sh":  scripts.InstallTemplates,
	}
	for name, content := range staticFiles {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	return nil
}

// WriteDevmDirStaticOnly regenerates the .devm/.env without touching
// the per-template installer scripts under .devm/templates/. This
// preserves the on-disk installers as the "last-applied" snapshot so
// that a subsequent ComputeTemplateChanges call can still detect
// source-file changes that haven't been applied yet.
//
// Use this in the reconcile pre-diff step for running sandboxes. Use
// WriteDevmDir everywhere else (cold start, recreate, explicit re-render).
func WriteDevmDirStaticOnly(cfg schema.Config, repoRoot string) error {
	return WriteDevmEnv(cfg, repoRoot)
}

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
