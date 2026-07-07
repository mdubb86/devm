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
  id: test
  vm_name: test-vm
services:
  webapp:
    port: 3000
    hostname: test.test
`)

	cfg, err := Load(dir)
	assert.NoError(t, err)
	assert.Equal(t, "test", cfg.Project.ID)
	assert.Equal(t, 3000, cfg.Services["webapp"].Port)
}

func TestLoadWithOverride_Proxy(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "devm.yaml", `
project:
  id: test
  vm_name: test-vm
`)
	writeFile(t, dir, "devm.me.yaml", `
project:
  proxy: none
`)

	cfg, err := Load(dir)
	assert.NoError(t, err)
	assert.Equal(t, "none", cfg.Project.Proxy)
}

func TestLoadResolvesEnvAndInjectsWorkspaceAndIsSandbox(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "devm.yaml", `
project:
  id: test
  vm_name: test-vm
env:
  CLAUDE_CONFIG_DIR: $WORKSPACE/.claude
`)

	cfg, err := Load(dir)
	assert.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, ".claude"), cfg.Env["CLAUDE_CONFIG_DIR"].Literal,
		"$WORKSPACE must be expanded by Load via ResolveEnv")
	assert.Equal(t, dir, cfg.Env["WORKSPACE"].Literal,
		"WORKSPACE must be injected by Load via ResolveEnv")
	assert.Equal(t, "1", cfg.Env["IS_SANDBOX"].Literal,
		"IS_SANDBOX must be injected by Load via ResolveEnv")
}

func TestLoadReportsReservedEnvKeyError(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "devm.yaml", `
project:
  id: test
  vm_name: test-vm
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
  id: foo
  vm_name: foo-vm
  hostname_apex: foo.local
`)

	_, err := Load(dir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "hostname_apex is no longer supported")
	assert.Contains(t, err.Error(), "HOSTNAME_APEX")
	assert.Contains(t, err.Error(), "devm.yaml",
		"error should identify which file is offending")
}

func TestLoad_RejectsLegacyHostnameApex_InOverride(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "devm.yaml", `
project:
  id: foo
  vm_name: foo-vm
`)
	writeFile(t, dir, "devm.me.yaml", `
project:
  hostname_apex: foo.local
`)

	_, err := Load(dir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "hostname_apex is no longer supported")
	assert.Contains(t, err.Error(), "HOSTNAME_APEX")
	assert.Contains(t, err.Error(), "devm.me.yaml",
		"error should identify the override file as offending")
}

func TestLoad_RejectsUnknownTopLevelField_InBase(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "devm.yaml", `
project:
  id: foo
  vm_name: foo-vm
volumes:
  /data: 1G
`)

	_, err := Load(dir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), `unknown field`)
	assert.Contains(t, err.Error(), `volumes`)
	assert.Contains(t, err.Error(), `devm.yaml`,
		"error should identify which file is offending")
}

func TestLoad_RejectsUnknownTopLevelField_InOverride(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "devm.yaml", `
project:
  id: foo
  vm_name: foo-vm
`)
	writeFile(t, dir, "devm.me.yaml", `
volumes:
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
  id: foo
  vm_name: foo-vm
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
  id: foo
  vm_name: foo-vm
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
  id: foo
  vm_name: foo-vm
  proxxy: on
`,
			wantIn: "proxxy",
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

func TestLoadStrictFailsOnMissingRequiredField(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "devm.yaml", `
project:
  id: test
  # missing vm_name
`)

	_, err := Load(dir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "vm_name")
}
