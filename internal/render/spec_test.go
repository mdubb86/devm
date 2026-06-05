package render

import (
	"strings"
	"testing"

	"github.com/mtwaage/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// parsedSpec is a minimal mirror of the rendered kit spec, sufficient
// for structural test assertions. Tests parse the marshaled output back
// through yaml.Unmarshal rather than substring-matching specific
// quoting styles, which let us swap the renderer from string-builder
// to yaml.Marshal without rewriting every assertion against the new
// quoting.
type parsedSpec struct {
	SchemaVersion string `yaml:"schemaVersion"`
	Agent         struct {
		Entrypoint struct {
			Run []string `yaml:"run"`
		} `yaml:"entrypoint"`
	} `yaml:"agent"`
	Commands struct {
		Install []struct {
			Command string `yaml:"command"`
		} `yaml:"install"`
		Startup []struct {
			Command     []string `yaml:"command"`
			User        string   `yaml:"user"`
			Description string   `yaml:"description"`
		} `yaml:"startup"`
	} `yaml:"commands"`
}

func parseSpec(t *testing.T, raw string) parsedSpec {
	t.Helper()
	var p parsedSpec
	require.NoError(t, yaml.Unmarshal([]byte(raw), &p), "rendered spec.yaml must parse: %s", raw)
	return p
}

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
				Port: 3000,
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

func TestSpecYAMLEntrypointIsShellWrappedSleep(t *testing.T) {
	cfg := minimalConfig(t)
	out := SpecYAML(cfg, "/tmp/repo")
	// The entrypoint wraps sleep infinity in `sh -c "exec ... </dev/null"`.
	// The shell wrapping is required for sbx session-end cleanup
	// propagation; the </dev/null redirect detaches sleep from the
	// pty sbx allocates for the anchor, so it doesn't appear as a
	// phantom session in devm stop's session listing. See the comment
	// in spec.go for the full rationale.
	parsed := parseSpec(t, out)
	assert.Equal(t,
		[]string{"sh", "-c", "exec sleep infinity </dev/null"},
		parsed.Agent.Entrypoint.Run,
	)
	assert.NotContains(t, out, "devm-anchor.pid", "pidfile mechanism was dropped — it was a no-op")
	assert.NotContains(t, out, "/usr/local/bin/devm-agent", "no devm-agent binary in this design")
	assert.NotContains(t, out, "background: true", "no background daemons in this design")
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
	cfg.Install = []string{
		"apt-get update && apt-get install -y jq",
		"npm install -g typescript",
	}
	out := SpecYAML(cfg, "/tmp/repo")

	assert.Contains(t, out, "install:")
	assert.Contains(t, out, "apt-get update && apt-get install -y jq")
	assert.Contains(t, out, "npm install -g typescript")

	// Verify declaration order.
	jqIdx := strings.Index(out, "apt-get update")
	tsIdx := strings.Index(out, "npm install")
	assert.Greater(t, tsIdx, jqIdx, "install steps must render in declaration order")

	// No provision.sh referenced.
	assert.NotContains(t, out, "provision.sh")

	// No user: or description: on user-defined install steps.
	// (init-volumes still has user: "1000" and a description — that's hardcoded.)
}

func TestSpecYAMLInstallCommandPreservesSingleQuotes(t *testing.T) {
	cfg := minimalConfig(t)
	cfg.Install = []string{
		`echo 'hello world'`,
	}
	out := SpecYAML(cfg, "/tmp/repo")
	// Round-trip parse: whatever quoting style yaml.v3 chooses, the
	// install command must come back exactly as the user wrote it.
	parsed := parseSpec(t, out)
	require.Len(t, parsed.Commands.Install, 1)
	assert.Equal(t, `echo 'hello world'`, parsed.Commands.Install[0].Command)
}

func TestSpecYAMLStartupOnlyInitVolumesWhenNoServiceStartup(t *testing.T) {
	cfg := minimalConfig(t)
	out := SpecYAML(cfg, "/tmp/repo")
	assert.Contains(t, out, "init-volumes.sh")
	startupSection := extractStartupSection(t, out)
	// Two built-in startup steps: init-volumes + install-templates.
	assert.Equal(t, 2, strings.Count(startupSection, "- command:"))
}

func TestSpecYAMLAggregatesServiceStartupInSortedOrder(t *testing.T) {
	cfg := minimalConfig(t)
	cfg.Services = map[string]schema.Service{
		"redis": {
			Port: 6379,
			Startup: []schema.StartupCommand{
				{Command: []string{"redis-server", "/etc/redis.conf"}, Background: true},
			},
		},
		"postgres": {
			Port: 5432,
			Startup: []schema.StartupCommand{
				{Command: []string{"pg_ctl", "start"}, Background: true},
				{Command: []string{"pg_isready"}},
			},
		},
	}
	out := SpecYAML(cfg, "/tmp/repo")
	startupSection := extractStartupSection(t, out)

	// 2 built-in (init-volumes + install-templates) + 2 postgres + 1 redis = 5 steps.
	assert.Equal(t, 5, strings.Count(startupSection, "- command:"))

	// Service sort order is alphabetical: postgres before redis.
	pgStartIdx := strings.Index(startupSection, "pg_ctl")
	pgReadyIdx := strings.Index(startupSection, "pg_isready")
	redisIdx := strings.Index(startupSection, "redis-server")

	require.Positive(t, pgStartIdx)
	require.Positive(t, pgReadyIdx)
	require.Positive(t, redisIdx)
	assert.Less(t, pgStartIdx, pgReadyIdx, "postgres steps in declaration order")
	assert.Less(t, pgReadyIdx, redisIdx, "postgres service comes before redis")

	// Background daemons are rendered as foreground sbx kit steps wrapped
	// with shell-level `nohup ... &`. The kit's own `background: true`
	// flag is NOT used (it kills the process after ~5s — see
	// docs/sbx-quirks.md quirk #5).
	assert.Contains(t, startupSection, "nohup")
	assert.NotContains(t, startupSection, "background: true")
}

func TestSpecYAMLStartupCommandArrayRoundTrips(t *testing.T) {
	cfg := minimalConfig(t)
	cfg.Services = map[string]schema.Service{
		"web": {
			Port: 3000,
			Startup: []schema.StartupCommand{
				{Command: []string{"node", "server.js", "--port", "3000"}},
			},
		},
	}
	out := SpecYAML(cfg, "/tmp/repo")
	// Belt: the rendered command argv parses back to the exact argv,
	// including the "3000" string (yaml.v3 quotes it on output to
	// preserve the type — verifying via round-trip is what we care
	// about, not the specific output quoting style).
	parsed := parseSpec(t, out)
	require.GreaterOrEqual(t, len(parsed.Commands.Startup), 3, "need at least built-ins + web step")
	webStep := parsed.Commands.Startup[len(parsed.Commands.Startup)-1]
	assert.Equal(t,
		[]string{"node", "server.js", "--port", "3000"},
		webStep.Command,
	)
	// Suspenders: flow style still gets emitted (sbx kits use it). A
	// regression to block style would make spec.yaml much noisier.
	assert.Contains(t, out, "- command: [node, server.js, --port,")
}

func TestSpecYAML_HasInstallTemplatesStartupStep(t *testing.T) {
	cfg := schema.Config{
		Project: schema.Project{ID: "x", SandboxName: "x-sbx", HostnameApex: "x.local"},
	}
	out := SpecYAML(cfg, "/tmp")
	require.Contains(t, out, "install-templates.sh")

	// Must appear AFTER init-volumes.sh.
	iv := strings.Index(out, "init-volumes.sh")
	it := strings.Index(out, "install-templates.sh")
	require.Greater(t, it, iv, "install-templates.sh must come after init-volumes.sh; got iv=%d it=%d", iv, it)

	// Runs as root.
	itLine := out[it : it+strings.Index(out[it:], "\n")]
	end := it + 200
	if end > len(out) {
		end = len(out)
	}
	require.Contains(t, out[it:end], "user: \"0\"")
	_ = itLine
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
