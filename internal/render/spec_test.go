package render

import (
	"strings"
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
	assert.NotContains(t, out, "/usr/local/bin/devm-agent")
	assert.NotContains(t, out, ".devm/devm-agent")
	// provision.sh is gone too; install block is empty when no user steps.
	assert.NotContains(t, out, "provision.sh")
}

func TestSpecYAMLOmitsInstallWhenEmpty(t *testing.T) {
	cfg := minimalConfig(t)
	out := SpecYAML(cfg, "/tmp/repo")
	// No install commands → no `install:` block in spec.yaml.
	assert.NotContains(t, out, "install:")
	// commands.startup still present (init-volumes lives there).
	assert.Contains(t, out, "startup:")
}

func TestSpecYAMLRendersUserInstallSteps(t *testing.T) {
	cfg := minimalConfig(t)
	cfg.Install = []schema.InstallCommand{
		{Command: "apt-get update && apt-get install -y jq", User: "0", Description: "Install jq"},
		{Command: "npm install -g typescript", User: "1000", Description: "Install TypeScript"},
	}
	out := SpecYAML(cfg, "/tmp/repo")

	assert.Contains(t, out, "install:")
	assert.Contains(t, out, "apt-get update && apt-get install -y jq")
	assert.Contains(t, out, `user: "0"`)
	assert.Contains(t, out, "Install jq")
	assert.Contains(t, out, "npm install -g typescript")
	assert.Contains(t, out, `user: "1000"`)
	assert.Contains(t, out, "Install TypeScript")

	// Verify declaration order.
	jqIdx := strings.Index(out, "Install jq")
	tsIdx := strings.Index(out, "Install TypeScript")
	assert.Greater(t, tsIdx, jqIdx, "install steps must render in declaration order")

	// Verify provision.sh is NOT referenced anywhere.
	assert.NotContains(t, out, "provision.sh")
}
