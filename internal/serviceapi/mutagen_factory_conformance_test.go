package serviceapi_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestMutagenCLIConstructionGoesThroughFactory scans every non-test .go
// file under devm's repo root for direct `&mutagen.CLI{` constructions
// and fails if any live outside serviceapi.NewMutagenCLI itself.
//
// Why this exists: mutagen's own CLI auto-spawns a daemon when it can't
// reach one, inheriting the calling process's env. A CLI constructed
// without MUTAGEN_SSH_PATH in its ExtraEnv auto-spawns a daemon whose
// SSH transport falls through to the system ssh client, and every later
// sync create fails on hostname resolution because the guest is only
// reachable via the tart-mutagen-ssh shim. Six sites in devm's tree
// missed that env at various points — hidden until an in-flight test
// SIGKILL'd the mutagen daemon and the next test's cold-start observed
// the auto-spawned replacement.
//
// NewMutagenCLI centralizes the env + data dir + exec setup; this test
// keeps any new caller from bypassing it.
func TestMutagenCLIConstructionGoesThroughFactory(t *testing.T) {
	root := repoRootFromCWD(t)
	pattern := regexp.MustCompile(`&mutagen\.CLI\{`)

	// The one authorized site — the factory itself.
	authorized := map[string]bool{
		filepath.Join(root, "internal", "serviceapi", "mutagen.go"): true,
	}

	var offenders []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// Skip vendored/cache directories that aren't ours to police.
			base := info.Name()
			if base == ".git" || base == "vendor" || base == "node_modules" ||
				base == ".claude" || base == ".superpowers" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !pattern.Match(body) {
			return nil
		}
		if authorized[path] {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		offenders = append(offenders, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if len(offenders) > 0 {
		t.Fatalf(
			"direct `&mutagen.CLI{...}` construction found in %d file(s): %v — "+
				"use serviceapi.NewMutagenCLI(cfg, bin, exec) instead; it sets "+
				"MUTAGEN_SSH_PATH so an auto-spawned mutagen daemon inherits "+
				"the tart-mutagen-ssh shim env",
			len(offenders), offenders,
		)
	}
}

// repoRootFromCWD walks up from the test's cwd until it finds a
// directory containing go.mod, and returns it. This keeps the test
// working whether it runs under `go test ./internal/serviceapi/…` or a
// broader `go test ./...` — no hard-coded repo path.
func repoRootFromCWD(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found walking up from %s", dir)
		}
		dir = parent
	}
}
