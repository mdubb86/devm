package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
	assert.NoError(t, err)
}

func TestLoadBaseOnly(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "devm.yaml", `
project:
  name: test
services:
  webapp:
    port: 3000
    hostname: test.test
`)

	cfg, err := Load(dir)
	assert.NoError(t, err)
	assert.Equal(t, "test", cfg.Project.Name)
	assert.Equal(t, 3000, cfg.Services["webapp"].Port)
}

// Reproduces a real bug hit during a shelfmates cold-start:
//
//   scripts:
//     install-gsd-core:
//       - cd "$WORKSPACE" && npx ...
//
//   provisioning fails with:
//     bash: line 1: cd: /Users/michael/workspace/shelfmates:
//                       No such file or directory
//
// The `cd "$WORKSPACE"` line runs INSIDE THE GUEST. The guest resolves
// $WORKSPACE from its shell env, which is populated from /etc/environment
// (see internal/render/etc_environment.go), which is emitted from
// cfg.Env["WORKSPACE"]. Load must set that env value to a path the guest
// can chdir into — under mutagen-volumes the guest workspace lives at
// `/home/devm/<primary-label>`, NOT the Mac cwd (there's no virtiofs
// mirror any more; the Mac's absolute path doesn't exist in the guest).
//
// Test uses an explicit `label:` on the primary so the expected guest
// path is known ahead of time (the URL-omitted-primary default label is
// filepath.Base(macCwd), a tempdir name that would be awkward to spell
// here).
func TestLoad_WorkspaceEnvIsPrimaryRepoGuestPath(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "devm.yaml", `
project:
  name: test
repos:
  main:
    label: work
    secret: gh_token
env:
  CLAUDE_CONFIG_DIR: $WORKSPACE/.claude
`)

	cfg, err := Load(dir)
	require.NoError(t, err)
	assert.Equal(t, "/home/devm/work", cfg.Env["WORKSPACE"].Literal,
		"WORKSPACE must resolve to the primary repo's GUEST path — /home/devm/<label> — not the Mac cwd")
	assert.Equal(t, "/home/devm/work/.claude", cfg.Env["CLAUDE_CONFIG_DIR"].Literal,
		"$WORKSPACE-composed values inherit the same guest-path substitution")
	assert.Equal(t, "1", cfg.Env["IS_SANDBOX"].Literal,
		"IS_SANDBOX must be injected by Load via ResolveEnv")
}

// Utility VMs with no `repos:` still need a valid WORKSPACE — scripts
// may reference $WORKSPACE without any repo declared. Fall back to
// /home/devm so `cd "$WORKSPACE"` lands somewhere sane.
func TestLoad_WorkspaceEnvFallsBackToHomeWhenNoRepos(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "devm.yaml", `
project:
  name: test
env:
  CACHE_DIR: $WORKSPACE/.cache
`)

	cfg, err := Load(dir)
	require.NoError(t, err)
	assert.Equal(t, "/home/devm", cfg.Env["WORKSPACE"].Literal,
		"WORKSPACE must fall back to /home/devm when no repos are declared")
	assert.Equal(t, "/home/devm/.cache", cfg.Env["CACHE_DIR"].Literal)
}

func TestLoadReportsReservedEnvKeyError(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "devm.yaml", `
project:
  name: test
env:
  WORKSPACE: /tmp/sneaky
`)

	_, err := Load(dir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "WORKSPACE")
	assert.Contains(t, err.Error(), "reserved")
}

func TestLoad_RejectsLegacyHostnameApex_InBase(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "devm.yaml", `
project:
  name: foo
  hostname_apex: foo.local
`)

	_, err := Load(dir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown field")
	assert.Contains(t, err.Error(), "hostname_apex")
	assert.Contains(t, err.Error(), "devm.yaml",
		"error should identify which file is offending")
}

func TestLoad_RejectsUnknownProjectKey_InOverride(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "devm.yaml", `
project:
  name: foo
`)
	writeFile(t, dir, "devm.me.yaml", `
project:
  hostname_apex: foo.local
`)

	_, err := Load(dir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown field")
	assert.Contains(t, err.Error(), "hostname_apex")
	assert.Contains(t, err.Error(), "devm.me.yaml",
		"error should identify the override file as offending")
}

func TestLoad_RejectsUnknownTopLevelField_InBase(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "devm.yaml", `
project:
  name: foo
volumez:
  /data: 1G
`)

	_, err := Load(dir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), `unknown field`)
	assert.Contains(t, err.Error(), `volumez`)
	assert.Contains(t, err.Error(), `devm.yaml`,
		"error should identify which file is offending")
}

func TestLoad_RejectsUnknownTopLevelField_InOverride(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "devm.yaml", `
project:
  name: foo
`)
	writeFile(t, dir, "devm.me.yaml", `
volumez:
  /data: 1G
`)

	_, err := Load(dir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), `unknown field`)
	assert.Contains(t, err.Error(), `devm.me.yaml`)
}

// TestLoad_RejectsUnknownNestedField pins that yaml.v3's KnownFields(true)
// catches typos and removed fields ANYWHERE in the document, not just at
// the top level. Common examples: nested service field typos, a
// reintroduced-then-removed key, an unfamiliar block someone copied from
// a docs example.
func TestLoad_RejectsUnknownNestedField(t *testing.T) {
	cases := []struct {
		name, yaml, wantIn string
	}{
		{
			name: "unknown service field",
			yaml: `
project:
  name: foo
services:
  api:
    exec: ["/bin/true"]
    replicaz: 3
`,
			wantIn: "replicaz",
		},
		{
			name: "unknown network field",
			yaml: `
project:
  name: foo
network:
  allowlist:
    - example.com
`,
			wantIn: "allowlist",
		},
		{
			name: "typo inside project",
			yaml: `
project:
  name: foo
  proxxy: on
`,
			wantIn: "proxxy",
		},
		{
			// project.proxy was removed (F13): a devm.yaml still
			// carrying it must fail loud via the same unknown-field
			// path as any other removed/typo'd key, not silently no-op.
			name: "removed project.proxy field",
			yaml: `
project:
  name: foo
  proxy: caddy
`,
			wantIn: "proxy",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, "devm.yaml", tc.yaml)
			_, err := Load(dir)
			require.Error(t, err, "expected unknown-field rejection")
			assert.Contains(t, err.Error(), tc.wantIn,
				"error should name the offending key: %s", err)
		})
	}
}

func TestLoadMissingConfigIsErrNoConfig(t *testing.T) {
	_, err := Load(t.TempDir())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNoConfig)
}

func TestLoadInvalidConfigIsNotErrNoConfig(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "devm.yaml", `
project:
  name: test
  vm_name: legacy
`)

	_, err := Load(dir)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNoConfig,
		"an invalid devm.yaml must not be mistaken for a missing one")
}

func TestLoadStrictFailsOnMissingRequiredField(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "devm.yaml", `
project:
  # missing name
`)

	_, err := Load(dir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "name")
}

func TestLoad_ReposMap(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "devm.yaml", `
project:
  name: test
repos:
  main:
    secret: github
    primary: true
`)

	cfg, err := Load(dir)
	require.NoError(t, err)
	require.Contains(t, cfg.Repos, "main")
	assert.Equal(t, "github", cfg.Repos["main"].Secret)
}

func TestLoad_RejectsLegacyTopLevelRepo(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "devm.yaml", `
project:
  name: test
repo:
  secret: github
`)

	_, err := Load(dir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown field")
	assert.Contains(t, err.Error(), "repo")
}

func TestLoad_RejectsUnknownCommandField(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "devm.yaml", `
project:
  name: test
repos:
  main:
    secret: gh
    commands:
      install:
        exec: pnpm install
        run_on_setup: true
`)
	_, err := Load(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "run_on_setup")
}

func TestLoad_RepoCommandsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "devm.yaml", `
project:
  name: test
scripts:
  fmt-check:
    - echo fmt
repos:
  main:
    secret: gh
    commands:
      install:
        exec: pnpm install
        startup: true
      lint:
        exec: ">fmt-check"
`)
	cfg, err := Load(dir)
	require.NoError(t, err)
	require.Contains(t, cfg.Repos, "main")
	require.Contains(t, cfg.Repos["main"].Commands, "install")
	assert.Equal(t, "pnpm install", cfg.Repos["main"].Commands["install"].Exec)
	require.NotNil(t, cfg.Repos["main"].Commands["install"].Startup)
	assert.True(t, *cfg.Repos["main"].Commands["install"].Startup)
	assert.Equal(t, ">fmt-check", cfg.Repos["main"].Commands["lint"].Exec)
	assert.Nil(t, cfg.Repos["main"].Commands["lint"].Startup, "unspecified startup stays nil")
}

func TestReadProjectName(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "devm.yaml", `
project:
  name: myproj
services:
  webapp:
    port: 3000
    hostname: test.test
`)
	name, err := ReadProjectName(dir)
	require.NoError(t, err)
	assert.Equal(t, "myproj", name)
}

func TestReadProjectName_MissingFileIsErrNoConfig(t *testing.T) {
	_, err := ReadProjectName(t.TempDir())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNoConfig)
}

func TestReadProjectName_MissingNameErrors(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "devm.yaml", `
project:
  # missing name
`)
	_, err := ReadProjectName(dir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "name")
}

// TestReadProjectName_IgnoresContentThatWouldFailFullLoad proves
// ReadProjectName really is a minimal parse: it reads project.name
// even from a devm.yaml that Load rejects for an unrelated schema
// violation (here, the legacy project.vm_name field). Shell must not
// be blocked by devm.yaml content it doesn't otherwise care about.
func TestReadProjectName_IgnoresContentThatWouldFailFullLoad(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "devm.yaml", `
project:
  name: test
  vm_name: legacy
`)
	_, loadErr := Load(dir)
	require.Error(t, loadErr, "sanity check: this content must fail full Load")

	name, err := ReadProjectName(dir)
	require.NoError(t, err)
	assert.Equal(t, "test", name)
}
