package render

import (
	"strings"
	"testing"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
)

func TestRenderService_FullOverride_VerbatimReturn(t *testing.T) {
	override := `[Unit]
Description=custom
[Service]
ExecStart=/bin/true
Type=oneshot
`
	svc := schema.Service{Systemd: override}
	got := string(RenderService("api", svc))
	// Verbatim (with trailing newline normalized).
	assert.Equal(t, override, got)
}

func TestRenderService_FullOverride_NormalizesTrailingWhitespace(t *testing.T) {
	override := "[Unit]\nDescription=custom\n[Service]\nExecStart=/bin/true\n\n\n   \n"
	svc := schema.Service{Systemd: override}
	got := string(RenderService("api", svc))
	// Trimmed trailing whitespace + exactly one newline.
	assert.True(t, strings.HasSuffix(got, "ExecStart=/bin/true\n"))
	assert.False(t, strings.HasSuffix(got, "\n\n"))
}

func TestRenderService_Declarative_HasDefaults(t *testing.T) {
	svc := schema.Service{Exec: []string{"/usr/bin/npm", "run", "dev"}}
	got := string(RenderService("api", svc))

	assert.Contains(t, got, "[Unit]")
	assert.Contains(t, got, "Description=devm service: api")
	assert.Contains(t, got, "After=devm-ready.target")
	assert.Contains(t, got, "Requires=devm-ready.target")

	assert.Contains(t, got, "[Service]")
	assert.Contains(t, got, "ExecStart=/usr/bin/npm run dev")
	assert.Contains(t, got, "User=devm", "User defaults to devm (guest identity)")
	assert.Contains(t, got, "Restart=on-failure", "Restart defaults to on-failure")

	assert.Contains(t, got, "[Install]")
	assert.Contains(t, got, "WantedBy=multi-user.target")
}

func TestRenderService_Declarative_AllFields(t *testing.T) {
	svc := schema.Service{
		Exec:    []string{"/bin/sleep", "infinity"},
		WorkDir: "/var/lib/foo",
		User:    "appuser",
		Env:     map[string]schema.EnvValue{"LOG_LEVEL": {Literal: "debug"}, "API_KEY": {Literal: "x"}},
		After:   []string{"postgresql.service", "redis.service"},
		Restart: "always",
	}
	got := string(RenderService("worker", svc))

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
	svc := schema.Service{Exec: []string{"/bin/true"}}
	got := string(RenderService("x", svc))
	assert.NotContains(t, got, "Environment=")
}

func TestRenderService_Declarative_HostnameAndPortOnlyService(t *testing.T) {
	// Service block with only routing fields (no Exec, no Systemd).
	// The rendered unit will have an empty Service section — useful
	// for the orchestrator to detect "nothing to run" and skip
	// systemctl enable.
	svc := schema.Service{Hostname: "api.test", Port: 8080}
	got := string(RenderService("api", svc))
	assert.NotContains(t, got, "ExecStart=")
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
