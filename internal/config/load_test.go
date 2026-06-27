package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
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
  sandbox_name: test-sbx
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
  sandbox_name: test-sbx
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
  sandbox_name: test-sbx
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
  sandbox_name: test-sbx
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
  sandbox_name: foo-sbx
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
  sandbox_name: foo-sbx
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
  sandbox_name: foo-sbx
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
  sandbox_name: foo-sbx
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

func TestLoadStrictFailsOnMissingRequiredField(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "devm.yaml", `
project:
  id: test
  # missing sandbox_name
`)

	_, err := Load(dir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "sandbox_name")
}
