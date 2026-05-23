package render

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mtwaage/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteDevmDir(t *testing.T) {
	dir := t.TempDir()
	cfg := schema.Config{
		Project:   schema.Project{ID: "x", SandboxName: "x-sbx", HostnameApex: "x.local"},
		BaseImage: schema.BaseImage{Docker: true},
		Services: map[string]schema.Service{
			"webapp": {Canonical: 3000, Hostname: "x.local"},
		},
	}
	err := WriteDevmDir(cfg, dir)
	assert.NoError(t, err)

	for _, p := range []string{
		".devm/Caddyfile",
		".devm/spec.yaml",
		".devm/scripts/provision.sh",
		".devm/scripts/init-volumes.sh",
		".devm/scripts/devm-exec.sh",
	} {
		_, err := os.Stat(filepath.Join(dir, p))
		assert.NoError(t, err, "missing %s", p)
	}
}

func TestWriteDevmDirDoesNotIncludeAgentBinary(t *testing.T) {
	tmp := t.TempDir()
	cfg := minimalConfig(t)
	require.NoError(t, WriteDevmDir(cfg, tmp))

	agentPath := filepath.Join(tmp, ".devm", "devm-agent")
	_, err := os.Stat(agentPath)
	assert.True(t, os.IsNotExist(err),
		".devm/devm-agent must not be written; binary removed from design")
}
