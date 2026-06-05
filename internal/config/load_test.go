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
