package render

import (
	"strings"
	"testing"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// renderUnit calls RenderService with no extra wrapper env/path inputs
// and returns just the unit body, for tests that only care about the
// unit — the argv-form and full-override shapes exercised below never
// populate wrapperPath/wrapperBody.
func renderUnit(t *testing.T, name string, svc schema.Service) string {
	t.Helper()
	unit, _, _, err := RenderService(svc, name, nil, nil)
	require.NoError(t, err)
	return string(unit)
}

func TestRenderService_ExecFuncEmitsWrapper(t *testing.T) {
	unit, wrapperPath, wrapperBody, err := RenderService(schema.Service{
		ExecFunc: "run-worker",
	}, "worker", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "/opt/devm/service-wrappers/worker.sh", wrapperPath)
	assert.Contains(t, string(unit), "ExecStart=/opt/devm/service-wrappers/worker.sh")
	assert.Contains(t, string(wrapperBody), "source /home/devm/devm.sh")
	assert.Contains(t, string(wrapperBody), "run-worker")
}

func TestRenderService_ExecArgvNoWrapper(t *testing.T) {
	unit, wrapperPath, _, err := RenderService(schema.Service{
		ExecArgv: []string{"/usr/bin/foo", "--arg"},
	}, "cache", nil, nil)
	require.NoError(t, err)
	assert.Empty(t, wrapperPath, "argv shape must not emit a wrapper")
	assert.Contains(t, string(unit), "ExecStart=/usr/bin/foo --arg")
}

func TestRenderService_FullOverride_VerbatimReturn(t *testing.T) {
	override := `[Unit]
Description=custom
[Service]
ExecStart=/bin/true
Type=oneshot
`
	svc := schema.Service{Systemd: override}
	got := renderUnit(t, "api", svc)
	// Verbatim (with trailing newline normalized).
	assert.Equal(t, override, got)
}

func TestRenderService_FullOverride_NormalizesTrailingWhitespace(t *testing.T) {
	override := "[Unit]\nDescription=custom\n[Service]\nExecStart=/bin/true\n\n\n   \n"
	svc := schema.Service{Systemd: override}
	got := renderUnit(t, "api", svc)
	// Trimmed trailing whitespace + exactly one newline.
	assert.True(t, strings.HasSuffix(got, "ExecStart=/bin/true\n"))
	assert.False(t, strings.HasSuffix(got, "\n\n"))
}

func TestRenderService_Declarative_HasDefaults(t *testing.T) {
	svc := schema.Service{ExecArgv: []string{"/usr/bin/npm", "run", "dev"}}
	got := renderUnit(t, "api", svc)

	assert.Contains(t, got, "[Unit]")
	assert.Contains(t, got, "Description=devm service: api")
	assert.Contains(t, got, "After=devm-ready.target")
	assert.Contains(t, got, "Requires=devm-ready.target")

	assert.Contains(t, got, "[Service]")
	assert.Contains(t, got, "ExecStart=/usr/bin/npm run dev")
	assert.Contains(t, got, "User=devm", "User defaults to devm (guest identity)")
	assert.Contains(t, got, "Restart=on-failure", "Restart defaults to on-failure")

	assert.Contains(t, got, "[Install]")
	assert.Contains(t, got, "WantedBy=devm.target")
}

func TestRenderService_JoinsDevmTarget(t *testing.T) {
	out := renderUnit(t, "web", schema.Service{ExecArgv: []string{"run"}})
	require.Contains(t, out, "WantedBy=devm.target")
	require.NotContains(t, out, "devm-enforce.service")
}

func TestRenderService_Declarative_AllFields(t *testing.T) {
	svc := schema.Service{
		ExecArgv: []string{"/bin/sleep", "infinity"},
		WorkDir:  "/var/lib/foo",
		User:     "appuser",
		Env:      map[string]schema.EnvValue{"LOG_LEVEL": {Literal: "debug"}, "API_KEY": {Literal: "x"}},
		After:    []string{"postgresql.service", "redis.service"},
		Restart:  "always",
	}
	got := renderUnit(t, "worker", svc)

	assert.Contains(t, got, "WorkingDirectory=/var/lib/foo")
	assert.Contains(t, got, "User=appuser")
	// Env vars rendered in sorted key order for determinism.
	apiKeyIdx := strings.Index(got, "Environment=API_KEY=x")
	logLevelIdx := strings.Index(got, "Environment=LOG_LEVEL=debug")
	assert.Greater(t, apiKeyIdx, 0)
	assert.Greater(t, logLevelIdx, 0)
	assert.Less(t, apiKeyIdx, logLevelIdx, "env keys sorted alphabetically")

	assert.Contains(t, got, "After=devm-ready.target postgresql.service redis.service")
	assert.Contains(t, got, "Restart=always")
}

func TestRenderService_Declarative_NoEnv_OmitsEnvironmentLine(t *testing.T) {
	svc := schema.Service{ExecArgv: []string{"/bin/true"}}
	got := renderUnit(t, "x", svc)
	assert.NotContains(t, got, "Environment=")
}

func TestRenderService_Declarative_HostnameAndPortOnlyService(t *testing.T) {
	// Service block with only routing fields (no Exec, no Systemd).
	// The rendered unit will have an empty Service section — useful
	// for the orchestrator to detect "nothing to run" and skip
	// systemctl enable.
	svc := schema.Service{Hostname: "api.test", Port: 8080}
	got := renderUnit(t, "api", svc)
	assert.NotContains(t, got, "ExecStart=")
}

func TestRenderService_DeclarativeIncludesEnvironmentFile(t *testing.T) {
	svc := schema.Service{ExecArgv: []string{"run"}}
	got := renderUnit(t, "api", svc)
	assert.Contains(t, got, "EnvironmentFile=-/etc/environment\n")
}

func TestRenderService_FullOverride_NoEnvironmentFileAdded(t *testing.T) {
	// User-provided full override is verbatim — devm doesn't inject.
	svc := schema.Service{Systemd: "[Unit]\nDescription=raw\n\n[Service]\nExecStart=/bin/true\n"}
	got := renderUnit(t, "api", svc)
	assert.NotContains(t, got, "EnvironmentFile=-/etc/environment")
}

func TestRenderService_EnvironmentFileBeforeEnvironment(t *testing.T) {
	// Ordering matters: EnvironmentFile= is applied first, then
	// Environment= lines override for the same key. Per-service env
	// must beat /etc/environment.
	svc := schema.Service{
		ExecArgv: []string{"run"},
		Env: map[string]schema.EnvValue{
			"PORT": {Literal: "8080"},
		},
	}
	got := renderUnit(t, "api", svc)
	envFileIdx := strings.Index(got, "EnvironmentFile=-/etc/environment")
	envLineIdx := strings.Index(got, "Environment=PORT=8080")
	require.NotEqual(t, -1, envFileIdx)
	require.NotEqual(t, -1, envLineIdx)
	assert.Less(t, envFileIdx, envLineIdx,
		"EnvironmentFile= must appear before Environment= so per-service env overrides /etc/environment")
}

func TestSystemdQuoteArgv(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want string
	}{
		{"plain", []string{"/bin/echo", "hello"}, `/bin/echo hello`},
		{"whitespace in arg", []string{"sh", "-c", "touch /tmp/x"}, `sh -c "touch /tmp/x"`},
		{"single quote in arg", []string{"sh", "-c", "echo 'hi'"}, `sh -c "echo 'hi'"`},
		{"double quote in arg", []string{"sh", "-c", `echo "hi"`}, `sh -c "echo \"hi\""`},
		{"backslash in arg", []string{"sh", "-c", `printf %s\n foo`}, `sh -c "printf %%s\\n foo"`},
		{"empty argv", []string{}, ``},
		// systemd specifier escaping: bare % in argv would be substituted
		// by systemd (%s = user shell, %h = user home, …). devm doubles
		// them so the argv reaches the process verbatim.
		{"percent-s not consumed as specifier", []string{"printf", `%s`, "hi"}, `printf %%s hi`},
		{"double percent stays escaped", []string{"echo", "50%"}, `echo 50%%`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := systemdQuoteArgv(tc.argv)
			if got != tc.want {
				t.Fatalf("systemdQuoteArgv(%v)\n got: %q\nwant: %q", tc.argv, got, tc.want)
			}
		})
	}
}
