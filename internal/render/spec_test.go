package render

import (
	"testing"

	"github.com/mtwaage/devm/internal/schema"
	"github.com/stretchr/testify/assert"
)

func TestSpecYAMLBasic(t *testing.T) {
	cfg := schema.Config{
		Project: schema.Project{
			ID:           "test",
			SandboxName:  "test-sbx",
			HostnameApex: "test.local",
		},
		BaseImage: schema.BaseImage{Docker: true},
		Network:   schema.Network{AllowedDomains: []string{"github.com", "claude.ai"}},
		Services: map[string]schema.Service{
			"workspace": {
				Masks: []schema.Mask{
					{Path: "node_modules", Size: "2G"},
					{Path: ".turbo", Size: "500M"},
				},
			},
			"webapp": {
				Canonical: 3000,
				Hostname:  "test.local",
				Masks: []schema.Mask{
					{Path: "apps/web/node_modules", Size: "500M"},
				},
			},
		},
	}
	out := SpecYAML(cfg, "/Users/test/workspace/myproject")
	assert.Contains(t, out, "shell-docker")
	assert.Contains(t, out, "test")
	assert.Contains(t, out, "github.com")
	assert.Contains(t, out, "/Users/test/workspace/myproject/node_modules")
	assert.Contains(t, out, "size=2G")
	assert.Contains(t, out, "/Users/test/workspace/myproject/apps/web/node_modules")
}

func TestSpecYAMLNonDockerBaseUsesShellTemplate(t *testing.T) {
	cfg := schema.Config{
		Project:   schema.Project{ID: "x", SandboxName: "x-sbx", HostnameApex: "x.local"},
		BaseImage: schema.BaseImage{Docker: false},
	}
	out := SpecYAML(cfg, "/tmp/x")
	// docker/sandbox-templates:shell is the published tag (shell-only
	// does not exist on Docker Hub; confirmed empirically 2026-05-22).
	assert.Contains(t, out, "docker/sandbox-templates:shell\n")
	assert.NotContains(t, out, "shell-docker")
	assert.NotContains(t, out, "shell-only")
}

func minimalConfig(t *testing.T) schema.Config {
	t.Helper()
	return schema.Config{
		Project:   schema.Project{ID: "x", SandboxName: "x-sbx", HostnameApex: "x.local"},
		BaseImage: schema.BaseImage{Docker: false},
	}
}

func TestSpecYAMLEntrypointIsSleepInfinity(t *testing.T) {
	cfg := minimalConfig(t)
	out := SpecYAML(cfg, "/tmp/repo")
	// agent.entrypoint.run holds the sbx run session's pty open while
	// the host orchestrator does setup. sleep ignores stdin so the
	// process survives stdin redirection from our subprocess.
	assert.Contains(t, out, "entrypoint:")
	assert.Contains(t, out, `run: ["sleep", "infinity"]`)
	assert.NotContains(t, out, "/usr/local/bin/devm-agent",
		"the devm-agent binary has been removed; entrypoint must be sleep infinity")
	assert.NotContains(t, out, "background: true",
		"no background daemons in this design")
}

func TestSpecYAMLDoesNotInstallAgentBinary(t *testing.T) {
	cfg := minimalConfig(t)
	out := SpecYAML(cfg, "/tmp/repo")
	// commands.install no longer copies a devm-agent binary into /usr/local/bin.
	assert.NotContains(t, out, "/usr/local/bin/devm-agent")
	assert.NotContains(t, out, ".devm/devm-agent")
	// provision.sh install step remains.
	assert.Contains(t, out, `bash "$WORKSPACE_DIR/.devm/scripts/provision.sh"`)
}
