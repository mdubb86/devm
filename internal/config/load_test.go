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
  hostname_apex: test.local
base_image:
  docker: true
services:
  webapp:
    port: 3000
    hostname: test.local
`)

	cfg, err := Load(dir)
	assert.NoError(t, err)
	assert.Equal(t, "test", cfg.Project.ID)
	assert.Equal(t, 3000, cfg.Services["webapp"].Port)
}

func TestLoadWithOverride(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "devm.yaml", `
project:
  id: test
  sandbox_name: test-sbx
  hostname_apex: test.local
  port_offset: 0
base_image:
  docker: true
`)
	writeFile(t, dir, "devm.me.yaml", `
project:
  port_offset: 25
`)

	cfg, err := Load(dir)
	assert.NoError(t, err)
	assert.Equal(t, 25, cfg.Project.PortOffset)
}

func TestLoadResolvesEnvAndInjectsWorkspaceAndIsSandbox(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "devm.yaml", `
project:
  id: test
  sandbox_name: test-sbx
  hostname_apex: test.local
base_image:
  docker: true
env:
  CLAUDE_CONFIG_DIR: $WORKSPACE/.claude
`)

	cfg, err := Load(dir)
	assert.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, ".claude"), cfg.Env["CLAUDE_CONFIG_DIR"],
		"$WORKSPACE must be expanded by Load via ResolveEnv")
	assert.Equal(t, dir, cfg.Env["WORKSPACE"],
		"WORKSPACE must be injected by Load via ResolveEnv")
	assert.Equal(t, "1", cfg.Env["IS_SANDBOX"],
		"IS_SANDBOX must be injected by Load via ResolveEnv")
}

func TestLoadReportsReservedEnvKeyError(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "devm.yaml", `
project:
  id: test
  sandbox_name: test-sbx
  hostname_apex: test.local
base_image:
  docker: true
env:
  WORKSPACE: /tmp/sneaky
`)

	_, err := Load(dir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "WORKSPACE")
	assert.Contains(t, err.Error(), "reserved")
}

func TestLoadStrictFailsOnMissingRequiredField(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "devm.yaml", `
project:
  id: test
  # missing sandbox_name
base_image:
  docker: false
`)

	_, err := Load(dir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "sandbox_name")
}
