package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildRun compiles cmd/run once per test binary, returning the path.
// Uses the current GOOS/GOARCH so tests run on the developer's Mac.
func buildRun(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "run")
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Dir = "."
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build run: %v\n%s", err, b)
	}
	return out
}

func writeManifest(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, "commands.json")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	return p
}

// Manifest fixture used by most tests. Two repos, disambiguated by cwd.
const twoRepoManifest = `{
  "repos": {
    "main": {
      "guestPath": "MAIN",
      "commands": ["install", "test"]
    },
    "v1": {
      "guestPath": "V1",
      "commands": ["test"]
    }
  }
}`

// prepareTree writes the manifest with real cwds substituted for MAIN/V1,
// and mkdirs each. Returns (manifest path, mainDir, v1Dir).
func prepareTree(t *testing.T) (string, string, string) {
	t.Helper()
	base := t.TempDir()
	mainDir := filepath.Join(base, "main-repo")
	v1Dir := filepath.Join(base, "v1-repo")
	require.NoError(t, os.MkdirAll(filepath.Join(mainDir, "subdir"), 0o755))
	require.NoError(t, os.MkdirAll(v1Dir, 0o755))
	body := strings.NewReplacer("MAIN", mainDir, "V1", v1Dir).Replace(twoRepoManifest)
	return writeManifest(t, base, body), mainDir, v1Dir
}

func TestRun_ErrorNoArg(t *testing.T) {
	bin := buildRun(t)
	manifest, mainDir, _ := prepareTree(t)
	cmd := exec.Command(bin)
	cmd.Dir = mainDir
	cmd.Env = append(os.Environ(), "DEVM_COMMANDS_MANIFEST="+manifest)
	out, err := cmd.CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(out), "usage: run <command>")
	assert.Equal(t, 2, cmd.ProcessState.ExitCode())
}

func TestRun_ErrorOutsideRepo(t *testing.T) {
	bin := buildRun(t)
	manifest, _, _ := prepareTree(t)
	stray := t.TempDir() // outside both repos
	cmd := exec.Command(bin, "install")
	cmd.Dir = stray
	cmd.Env = append(os.Environ(), "DEVM_COMMANDS_MANIFEST="+manifest)
	out, err := cmd.CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(out), "run: not inside a registered repo")
}

func TestRun_ErrorUnknownCommand(t *testing.T) {
	bin := buildRun(t)
	manifest, mainDir, _ := prepareTree(t)
	cmd := exec.Command(bin, "bogus")
	cmd.Dir = mainDir
	cmd.Env = append(os.Environ(), "DEVM_COMMANDS_MANIFEST="+manifest)
	out, err := cmd.CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(out), "run: command bogus not registered in repo main")
}

func TestRun_ErrorFromNestedCwd_StillResolvesRepo(t *testing.T) {
	// The command is unregistered, but the error must still name "main" —
	// proof that findRepo walked up from the nested cwd to the repo root
	// before checking registration.
	bin := buildRun(t)
	manifest, mainDir, _ := prepareTree(t)
	cmd := exec.Command(bin, "bogus")
	cmd.Dir = filepath.Join(mainDir, "subdir")
	cmd.Env = append(os.Environ(), "DEVM_COMMANDS_MANIFEST="+manifest)
	out, err := cmd.CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(out), "run: command bogus not registered in repo main")
}

// TestRun_EmptyCommandsRepo_ErrorsNoCommand is a regression test:
// render.RenderCommandsManifest used to omit a repo with zero declared
// commands from the manifest entirely, so a cwd inside such a repo hit
// the "not inside a registered repo" branch — misleading, since the cwd
// IS in a devm repo. With the repo present in the manifest (commands:
// []), the same lookup must instead report "not registered" against the
// correct repo name.
func TestRun_EmptyCommandsRepo_ErrorsNoCommand(t *testing.T) {
	bin := buildRun(t)
	base := t.TempDir()
	mainDir := filepath.Join(base, "main-repo")
	require.NoError(t, os.MkdirAll(mainDir, 0o755))
	manifest := writeManifest(t, base, strings.NewReplacer("MAIN", mainDir).Replace(`{
	  "repos": {
	    "main": {
	      "guestPath": "MAIN",
	      "commands": []
	    }
	  }
	}`))

	cmd := exec.Command(bin, "install")
	cmd.Dir = mainDir
	cmd.Env = append(os.Environ(), "DEVM_COMMANDS_MANIFEST="+manifest)
	out, err := cmd.CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(out), "run: command install not registered in repo main")
}

// ---------- pure-logic unit tests (no subprocess, no guest filesystem) ----------

// resolvedTempDir returns a symlink-resolved t.TempDir() — main() resolves
// $PWD the same way before calling findRepo, and on macOS t.TempDir() lives
// under a symlink (/tmp -> /private/tmp), so tests must resolve it too or
// the guestPath comparison never matches.
func resolvedTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	return resolved
}

func TestFindRepo_WalksUpFromNestedCwd(t *testing.T) {
	base := resolvedTempDir(t)
	mainDir := filepath.Join(base, "main-repo")
	require.NoError(t, os.MkdirAll(filepath.Join(mainDir, "a", "b"), 0o755))

	var m manifest
	body := strings.NewReplacer("MAIN", mainDir).Replace(`{"repos":{"main":{"guestPath":"MAIN","commands":["install"]}}}`)
	require.NoError(t, json.Unmarshal([]byte(body), &m))

	repoName, guestPath, ok := findRepo(m, filepath.Join(mainDir, "a", "b"))
	require.True(t, ok)
	assert.Equal(t, "main", repoName)
	assert.Equal(t, mainDir, guestPath)
}

func TestFindRepo_OutsideAnyRepo(t *testing.T) {
	var m manifest
	require.NoError(t, json.Unmarshal([]byte(`{"repos":{"main":{"guestPath":"/nowhere","commands":["install"]}}}`), &m))
	_, _, ok := findRepo(m, t.TempDir())
	assert.False(t, ok)
}

func TestFindRepo_SameNameDifferentRepos_PicksByGuestPath(t *testing.T) {
	base := resolvedTempDir(t)
	mainDir := filepath.Join(base, "main-repo")
	v1Dir := filepath.Join(base, "v1-repo")
	require.NoError(t, os.MkdirAll(mainDir, 0o755))
	require.NoError(t, os.MkdirAll(v1Dir, 0o755))

	var m manifest
	body := strings.NewReplacer("MAIN", mainDir, "V1", v1Dir).Replace(twoRepoManifest)
	require.NoError(t, json.Unmarshal([]byte(body), &m))

	repoName, _, ok := findRepo(m, v1Dir)
	require.True(t, ok)
	assert.Equal(t, "v1", repoName)
}

func TestRegistered(t *testing.T) {
	assert.True(t, registered([]string{"install", "test"}, "test"))
	assert.False(t, registered([]string{"install", "test"}, "bogus"))
	assert.False(t, registered(nil, "install"))
}
