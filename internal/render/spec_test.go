package render

import (
	"strings"
	"testing"

	"github.com/mtwaage/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestSpecYAMLInstallCommandEscapesSingleQuote(t *testing.T) {
	cfg := minimalConfig(t)
	cfg.Install = []schema.InstallCommand{
		{Command: `echo 'hello world'`, Description: `Prints a 'greeting'`},
	}
	out := SpecYAML(cfg, "/tmp/repo")
	// Single quotes inside single-quoted YAML scalars are doubled.
	assert.Contains(t, out, `command: 'echo ''hello world'''`)
	assert.Contains(t, out, `description: 'Prints a ''greeting'''`)
}

func TestSpecYAMLInstallDescriptionWithColonStaysSafe(t *testing.T) {
	// A description with ": " would otherwise be parsed as a YAML mapping.
	cfg := minimalConfig(t)
	cfg.Install = []schema.InstallCommand{
		{Command: "true", Description: "Install jq: pretty printer"},
	}
	out := SpecYAML(cfg, "/tmp/repo")
	// Wrapping in single quotes is sufficient (no inner single quotes
	// to escape).
	assert.Contains(t, out, `description: 'Install jq: pretty printer'`)
}

func TestSpecYAMLStartupOnlyInitVolumesWhenNoServiceStartup(t *testing.T) {
	cfg := minimalConfig(t)
	out := SpecYAML(cfg, "/tmp/repo")
	assert.Contains(t, out, "init-volumes.sh")
	startupSection := extractStartupSection(t, out)
	// Only one startup step (the init-volumes one).
	assert.Equal(t, 1, strings.Count(startupSection, "- command:"))
}

func TestSpecYAMLAggregatesServiceStartupInSortedOrder(t *testing.T) {
	cfg := minimalConfig(t)
	cfg.Services = map[string]schema.Service{
		"redis": {
			Canonical: 6379,
			Startup: []schema.StartupCommand{
				{Command: []string{"redis-server", "/etc/redis.conf"}, Background: true, Description: "Start redis"},
			},
		},
		"postgres": {
			Canonical: 5432,
			Startup: []schema.StartupCommand{
				{Command: []string{"pg_ctl", "start"}, User: "postgres", Background: true, Description: "Start postgres step 1"},
				{Command: []string{"pg_isready"}, Description: "Wait for postgres"},
			},
		},
	}
	out := SpecYAML(cfg, "/tmp/repo")
	startupSection := extractStartupSection(t, out)

	// 1 init-volumes + 2 postgres + 1 redis = 4 steps.
	assert.Equal(t, 4, strings.Count(startupSection, "- command:"))

	// Service sort order is alphabetical: postgres before redis.
	pgIdx := strings.Index(startupSection, "Start postgres step 1")
	pgWaitIdx := strings.Index(startupSection, "Wait for postgres")
	redisIdx := strings.Index(startupSection, "Start redis")

	require.Positive(t, pgIdx)
	require.Positive(t, pgWaitIdx)
	require.Positive(t, redisIdx)
	assert.Less(t, pgIdx, pgWaitIdx, "postgres steps in declaration order within service")
	assert.Less(t, pgWaitIdx, redisIdx, "postgres service comes before redis")

	// Verify rendered shape includes background and the right user.
	assert.Contains(t, startupSection, "background: true")
	assert.Contains(t, startupSection, `user: "postgres"`)
}

func TestSpecYAMLStartupCommandArrayFormatting(t *testing.T) {
	cfg := minimalConfig(t)
	cfg.Services = map[string]schema.Service{
		"web": {
			Canonical: 3000,
			Startup: []schema.StartupCommand{
				{Command: []string{"node", "server.js", "--port", "3000"}},
			},
		},
	}
	out := SpecYAML(cfg, "/tmp/repo")
	// Argv array as YAML flow sequence with single-quoted elements.
	assert.Contains(t, out, `- command: ['node', 'server.js', '--port', '3000']`)
}

func TestSpecYAMLStartupDescriptionEscapesSingleQuote(t *testing.T) {
	cfg := minimalConfig(t)
	cfg.Services = map[string]schema.Service{
		"worker": {
			Startup: []schema.StartupCommand{
				{Command: []string{"worker"}, Description: `Run 'background' worker`},
			},
		},
	}
	out := SpecYAML(cfg, "/tmp/repo")
	assert.Contains(t, out, `description: 'Run ''background'' worker'`)
}

// extractStartupSection returns the contents of commands.startup (from
// the "  startup:" line through end of that indented section).
func extractStartupSection(t *testing.T, out string) string {
	t.Helper()
	startIdx := strings.Index(out, "  startup:")
	require.NotEqual(t, -1, startIdx, "spec.yaml has no startup block")
	rest := out[startIdx:]
	lines := strings.Split(rest, "\n")
	end := len(rest)
	col := 0
	for i, l := range lines {
		if i == 0 {
			col += len(l) + 1
			continue
		}
		// Top-level YAML key starts with no whitespace.
		if len(l) > 0 && l[0] != ' ' && l[0] != '\t' {
			end = col
			break
		}
		col += len(l) + 1
	}
	return rest[:end]
}
