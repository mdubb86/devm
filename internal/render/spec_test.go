package render

import (
	"strings"
	"testing"

	"github.com/mdubb86/devm/internal/schema"
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
			Background  bool     `yaml:"background"`
		} `yaml:"startup"`
	} `yaml:"commands"`
}

func parseSpec(t *testing.T, raw string) parsedSpec {
	t.Helper()
	var p parsedSpec
	require.NoError(t, yaml.Unmarshal([]byte(raw), &p), "rendered spec.yaml must parse: %s", raw)
	return p
}

func TestSpecYAMLKitEnvDoesNotContainIsSandbox(t *testing.T) {
	cfg := schema.Config{
		Project:   schema.Project{ID: "x", SandboxName: "x-sbx", HostnameApex: "x.local"},
		BaseImage: schema.BaseImage{Docker: true},
	}
	out := SpecYAML(cfg, "/tmp/x")
	// IS_SANDBOX moves to .devm/.env via PersistentEnv. Kit env.variables
	// stays empty so user cfg.Env changes can be BucketLive (kit env
	// requires teardown to update; .devm/.env doesn't).
	assert.NotContains(t, out, "IS_SANDBOX",
		"IS_SANDBOX must NOT appear in kit env; it lives in .devm/.env")
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
	// The entrypoint wraps sleep infinity in `bash -c "exec ... </dev/null"`.
	// The shell wrapping is required for sbx session-end cleanup
	// propagation; the </dev/null redirect detaches sleep from the
	// pty sbx allocates for the anchor, so it doesn't appear as a
	// phantom session in devm stop's session listing. See the comment
	// in spec.go for the full rationale.
	parsed := parseSpec(t, out)
	assert.Equal(t,
		[]string{"bash", "-c", "exec sleep infinity </dev/null"},
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
	// With no user-defined install steps, the rendered spec still has
	// the two framework steps: bootstrap (0), sentinel (1).
	// No cleanup step — install runs once on fresh /tmp at sandbox create.
	cfg := minimalConfig(t)
	out := SpecYAML(cfg, "/tmp/repo")
	parsed := parseSpec(t, out)
	assert.Equal(t, 2, len(parsed.Commands.Install),
		"empty cfg.Install must produce bootstrap + sentinel = 2 steps")
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

func TestSpecYAMLInstallStep0IsWrappedBootstrap(t *testing.T) {
	// No install cleanup step — bootstrap is now at index 0.
	cfg := minimalConfig(t)
	out := SpecYAML(cfg, "/tmp/repo")
	parsed := parseSpec(t, out)
	require.GreaterOrEqual(t, len(parsed.Commands.Install), 1)
	got := parsed.Commands.Install[0].Command
	assert.Contains(t, got, "wrap-fg.sh", "step 0 must invoke wrap-fg.sh")
	assert.Contains(t, got, "install 1", "step 0 must pass phase=install N=1")
	assert.Contains(t, got, "bootstrap.sh",
		"step 0's wrapped argv must invoke bootstrap.sh")
}

func TestSpecYAMLInstallUserStepWrapped(t *testing.T) {
	cfg := minimalConfig(t)
	cfg.Install = []string{"echo hello"}
	out := SpecYAML(cfg, "/tmp/repo")
	parsed := parseSpec(t, out)
	// Steps: 0 bootstrap, 1 user, 2 sentinel = 3 total.
	require.Equal(t, 3, len(parsed.Commands.Install),
		"expected bootstrap + 1 user + sentinel = 3")
	user := parsed.Commands.Install[1].Command
	assert.Contains(t, user, "wrap-fg.sh", "user step must invoke wrap-fg.sh")
	assert.Contains(t, user, "install 2", "user step must be index 2")
	assert.Contains(t, user, "echo hello",
		"user step must contain the user's command verbatim")
}

func TestSpecYAMLInstallSentinelLast(t *testing.T) {
	cfg := minimalConfig(t)
	cfg.Install = []string{"echo a", "echo b"}
	out := SpecYAML(cfg, "/tmp/repo")
	parsed := parseSpec(t, out)
	last := parsed.Commands.Install[len(parsed.Commands.Install)-1].Command
	assert.Contains(t, last, "touch /tmp/.devm-install/install-all-ok",
		"last install step must be the install-all-ok sentinel")
}

func TestSpecYAMLInstallPreservesUserSingleQuotes(t *testing.T) {
	cfg := minimalConfig(t)
	cfg.Install = []string{`echo 'hello world'`}
	out := SpecYAML(cfg, "/tmp/repo")
	parsed := parseSpec(t, out)
	require.Equal(t, 3, len(parsed.Commands.Install))
	user := parsed.Commands.Install[1].Command
	// The wrapFGInstall function uses the '\'' shell escape to embed
	// single quotes inside a single-quoted bash -c argument. The literal
	// text "echo 'hello world'" becomes "echo '\''hello world'\''".
	// We verify both that the user's command text is present (echo +
	// hello world) and that the wrap-fg.sh wrapper is applied.
	assert.Contains(t, user, "echo", "user command must be present in wrapped step")
	assert.Contains(t, user, "hello world", "user argument must survive the wrap-fg.sh argv embedding")
	assert.Contains(t, user, "wrap-fg.sh", "user step must invoke wrap-fg.sh")
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

	// cleanup(0) + init-volumes(1) + install-templates(2) + 2 postgres + 1 redis + sentinel = 7 steps.
	assert.Equal(t, 7, strings.Count(startupSection, "- command:"))

	// Service sort order is alphabetical: postgres before redis.
	pgStartIdx := strings.Index(startupSection, "pg_ctl")
	pgReadyIdx := strings.Index(startupSection, "pg_isready")
	redisIdx := strings.Index(startupSection, "redis-server")

	require.Positive(t, pgStartIdx)
	require.Positive(t, pgReadyIdx)
	require.Positive(t, redisIdx)
	assert.Less(t, pgStartIdx, pgReadyIdx, "postgres steps in declaration order")
	assert.Less(t, pgReadyIdx, redisIdx, "postgres service comes before redis")

	// Background daemons emit the kit-native `background: true` field.
	// The pre-sbx-0.31 workaround that wrapped them in shell-level
	// `nohup ... &` is gone — quirk #4 (kit-flag 5s-kill) is fixed,
	// pinned by e2e/test_sbx_quirk_04_kit_background_true.py.
	assert.NotContains(t, startupSection, "nohup",
		"shell-level nohup wrap should be gone post-0.31 simplification")
	assert.Contains(t, startupSection, "background: true",
		"background daemons should emit the kit-native flag")
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

func TestSpecYAMLStartupStep0IsCleanup(t *testing.T) {
	cfg := minimalConfig(t)
	out := SpecYAML(cfg, "/tmp/repo")
	parsed := parseSpec(t, out)
	require.GreaterOrEqual(t, len(parsed.Commands.Startup), 1)
	joined := strings.Join(parsed.Commands.Startup[0].Command, " ")
	assert.Contains(t, joined, "rm -rf /tmp/.devm-startup",
		"startup step 0 must wipe /tmp/.devm-startup for marker freshness")
}

func TestSpecYAMLStartupStep1IsWrappedInitVolumes(t *testing.T) {
	cfg := minimalConfig(t)
	out := SpecYAML(cfg, "/tmp/repo")
	parsed := parseSpec(t, out)
	require.GreaterOrEqual(t, len(parsed.Commands.Startup), 2)
	cmd := parsed.Commands.Startup[1].Command
	require.GreaterOrEqual(t, len(cmd), 6, "wrap-fg.sh argv form expected")
	assert.Equal(t, "bash", cmd[0])
	assert.Contains(t, cmd[1], "wrap-fg.sh")
	assert.Equal(t, "startup", cmd[2])
	assert.Equal(t, "1", cmd[3])
	assert.Equal(t, "--", cmd[4])
	// Trailing argv invokes init-volumes.sh.
	tail := strings.Join(cmd[5:], " ")
	assert.Contains(t, tail, "init-volumes.sh")
}

func TestSpecYAMLStartupStep2IsWrappedInstallTemplates(t *testing.T) {
	cfg := minimalConfig(t)
	out := SpecYAML(cfg, "/tmp/repo")
	parsed := parseSpec(t, out)
	require.GreaterOrEqual(t, len(parsed.Commands.Startup), 3)
	cmd := parsed.Commands.Startup[2].Command
	assert.Equal(t, "startup", cmd[2])
	assert.Equal(t, "2", cmd[3])
	tail := strings.Join(cmd[5:], " ")
	assert.Contains(t, tail, "install-templates.sh")
}

func TestSpecYAMLStartupUserFGStepWrapped(t *testing.T) {
	cfg := minimalConfig(t)
	cfg.Services = map[string]schema.Service{
		"web": {Port: 80, Startup: []schema.StartupCommand{
			{Command: []string{"node", "server.js"}},
		}},
	}
	out := SpecYAML(cfg, "/tmp/repo")
	parsed := parseSpec(t, out)
	// Steps: 0 cleanup, 1 init-volumes, 2 install-templates, 3 user, 4 sentinel
	require.Equal(t, 5, len(parsed.Commands.Startup))
	cmd := parsed.Commands.Startup[3].Command
	assert.Contains(t, cmd[1], "wrap-fg.sh",
		"foreground user step must invoke wrap-fg.sh")
	assert.Equal(t, "3", cmd[3], "user step index starts at 3")
	assert.Equal(t, []string{"node", "server.js"}, cmd[5:],
		"user argv must be the trailing slice")
}

func TestSpecYAMLStartupUserBGStepUsesWrapBG(t *testing.T) {
	cfg := minimalConfig(t)
	cfg.Services = map[string]schema.Service{
		"daemon": {Port: 9000, Startup: []schema.StartupCommand{
			{Command: []string{"my-daemon"}, Background: true},
		}},
	}
	out := SpecYAML(cfg, "/tmp/repo")
	parsed := parseSpec(t, out)
	cmd := parsed.Commands.Startup[3].Command
	assert.Contains(t, cmd[1], "wrap-bg.sh",
		"background user step must invoke wrap-bg.sh")
}

func TestSpecYAMLStartupUserBGPreservesBackgroundField(t *testing.T) {
	cfg := minimalConfig(t)
	cfg.Services = map[string]schema.Service{
		"daemon": {Port: 9000, Startup: []schema.StartupCommand{
			{Command: []string{"my-daemon"}, Background: true},
		}},
	}
	out := SpecYAML(cfg, "/tmp/repo")
	parsed := parseSpec(t, out)
	assert.True(t, parsed.Commands.Startup[3].Background,
		"Background: true must be preserved on the wrapped kit step")
}

func TestSpecYAMLStartupSentinelLast(t *testing.T) {
	cfg := minimalConfig(t)
	out := SpecYAML(cfg, "/tmp/repo")
	parsed := parseSpec(t, out)
	last := parsed.Commands.Startup[len(parsed.Commands.Startup)-1].Command
	joined := strings.Join(last, " ")
	assert.Contains(t, joined, "touch /tmp/.devm-startup/startup-all-ok",
		"last startup step must be the startup-all-ok sentinel")
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
