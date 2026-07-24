package render

import (
	"strings"
	"testing"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEtcEnvironmentQuote_TableDriven(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"empty", "", `""`, false},
		{"alnum", "abc123", `abc123`, false},
		{"path", "/opt/devm/bin", `/opt/devm/bin`, false},
		{"colon_at_dash_dot_slash_underscore", "user@host:8080/path.v2-alpha_x", `user@host:8080/path.v2-alpha_x`, false},
		{"space", "foo bar", `"foo bar"`, false},
		{"double_quote", `he said "hi"`, `"he said \"hi\""`, false},
		{"backslash", `a\b`, `"a\\b"`, false},
		{"hash", `#comment-like`, `"#comment-like"`, false},
		{"dollar_stays_literal", `$FOO`, `"$FOO"`, false},
		{"tab_escaped", "col1\tcol2", `"col1\tcol2"`, false},
		{"newline_rejected", "line1\nline2", "", true},
		{"cr_rejected", "line\r", "", true},
		{"nul_rejected", "a\x00b", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := etcEnvironmentQuote(tc.in)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestRenderEtcEnvironment_DefaultCfg(t *testing.T) {
	cfg := schema.Config{}
	body, err := RenderEtcEnvironment(cfg)
	require.NoError(t, err)
	assert.Contains(t, body, "NO_PROXY=*\n")
	assert.Contains(t, body, "NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/devm.crt\n")
	// No cfg.Path → PATH = /opt/devm/scripts:<guestSystemPATH>
	assert.Contains(t, body, `PATH="/opt/devm/scripts:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/usr/games"`+"\n")
}

func TestRenderEtcEnvironment_PathPrepend(t *testing.T) {
	cfg := schema.Config{Path: []string{"/workspace/bin", "/home/devm/.fnm/aliases/default/bin"}}
	body, err := RenderEtcEnvironment(cfg)
	require.NoError(t, err)
	assert.Contains(t, body, `PATH="/workspace/bin:/home/devm/.fnm/aliases/default/bin:/opt/devm/scripts:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/usr/games"`+"\n")
}

func TestRenderEtcEnvironment_UserEnv_BareAndQuoted(t *testing.T) {
	cfg := schema.Config{
		Env: map[string]schema.EnvValue{
			"SIMPLE":     {Literal: "value"},
			"WITH_SPACE": {Literal: "hello world"},
		},
	}
	body, err := RenderEtcEnvironment(cfg)
	require.NoError(t, err)
	assert.Contains(t, body, "SIMPLE=value\n")
	assert.Contains(t, body, `WITH_SPACE="hello world"`+"\n")
	// Deterministic sort: SIMPLE before WITH_SPACE.
	assert.Less(t, strings.Index(body, "SIMPLE="), strings.Index(body, "WITH_SPACE="))
}

func TestRenderEtcEnvironment_PerServiceEnv(t *testing.T) {
	cfg := schema.Config{
		Services: map[string]schema.Service{
			"api":    {Env: map[string]schema.EnvValue{"PORT": {Literal: "8080"}}},
			"worker": {Env: map[string]schema.EnvValue{"QUEUE": {Literal: "main"}}},
		},
	}
	body, err := RenderEtcEnvironment(cfg)
	require.NoError(t, err)
	assert.Contains(t, body, "API_PORT=8080\n")
	assert.Contains(t, body, "WORKER_QUEUE=main\n")
}

func TestRenderEtcEnvironment_RejectsNewlineValue(t *testing.T) {
	cfg := schema.Config{
		Env: map[string]schema.EnvValue{
			"BAD": {Literal: "line1\nline2"},
		},
	}
	_, err := RenderEtcEnvironment(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "BAD")
}

func TestRenderEtcEnvironment_Deterministic(t *testing.T) {
	cfg := schema.Config{
		Env: map[string]schema.EnvValue{
			"B_KEY": {Literal: "b"},
			"A_KEY": {Literal: "a"},
			"C_KEY": {Literal: "c"},
		},
	}
	body1, err := RenderEtcEnvironment(cfg)
	require.NoError(t, err)
	body2, err := RenderEtcEnvironment(cfg)
	require.NoError(t, err)
	assert.Equal(t, body1, body2)
	// Alphabetical order.
	iA := strings.Index(body1, "A_KEY=")
	iB := strings.Index(body1, "B_KEY=")
	iC := strings.Index(body1, "C_KEY=")
	assert.True(t, iA < iB && iB < iC)
}
