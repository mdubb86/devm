package main

import (
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
      "commands": {
        "install": {"exec": "echo main-install && pwd", "startup": true},
        "test":    {"exec": "echo main-test",           "startup": false}
      }
    },
    "v1": {
      "guestPath": "V1",
      "commands": {
        "test": {"exec": "echo v1-test", "startup": false}
      }
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

func TestRun_DispatchesFromCwd(t *testing.T) {
	bin := buildRun(t)
	manifest, mainDir, _ := prepareTree(t)

	cmd := exec.Command(bin, "install")
	cmd.Dir = mainDir
	cmd.Env = append(os.Environ(), "DEVM_COMMANDS_MANIFEST="+manifest)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "output: %s", out)
	assert.Contains(t, string(out), "main-install")
	assert.Contains(t, string(out), mainDir, "pwd should confirm run cd'd into the repo")
}

func TestRun_DispatchesFromNestedCwd(t *testing.T) {
	bin := buildRun(t)
	manifest, mainDir, _ := prepareTree(t)

	cmd := exec.Command(bin, "install")
	cmd.Dir = filepath.Join(mainDir, "subdir")
	cmd.Env = append(os.Environ(), "DEVM_COMMANDS_MANIFEST="+manifest)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "output: %s", out)
	assert.Contains(t, string(out), "main-install")
}

func TestRun_SameNameDifferentRepos(t *testing.T) {
	bin := buildRun(t)
	manifest, mainDir, v1Dir := prepareTree(t)

	fromMain := exec.Command(bin, "test")
	fromMain.Dir = mainDir
	fromMain.Env = append(os.Environ(), "DEVM_COMMANDS_MANIFEST="+manifest)
	mainOut, err := fromMain.CombinedOutput()
	require.NoError(t, err, "output: %s", mainOut)
	assert.Contains(t, string(mainOut), "main-test")

	fromV1 := exec.Command(bin, "test")
	fromV1.Dir = v1Dir
	fromV1.Env = append(os.Environ(), "DEVM_COMMANDS_MANIFEST="+manifest)
	v1Out, err := fromV1.CombinedOutput()
	require.NoError(t, err, "output: %s", v1Out)
	assert.Contains(t, string(v1Out), "v1-test")
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
	assert.Contains(t, string(out), "no devm repo in current directory")
}

func TestRun_ErrorUnknownCommand(t *testing.T) {
	bin := buildRun(t)
	manifest, mainDir, _ := prepareTree(t)
	cmd := exec.Command(bin, "bogus")
	cmd.Dir = mainDir
	cmd.Env = append(os.Environ(), "DEVM_COMMANDS_MANIFEST="+manifest)
	out, err := cmd.CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(out), `no command "bogus" in repo "main"`)
}
