package scriptfile

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderWrapper_MinimalCall(t *testing.T) {
	got, err := RenderWrapper(WrapperInput{FunctionName: "install"})
	require.NoError(t, err)
	assert.Contains(t, got, "#!/usr/bin/env bash")
	assert.Contains(t, got, "set -eo pipefail")
	assert.Contains(t, got, "source /home/devm/devm.sh")
	assert.Contains(t, got, "[ -f /home/devm/devm.me.sh ] && source /home/devm/devm.me.sh")
	// Function invocation is the last non-empty line.
	lines := nonEmptyLines(got)
	assert.Equal(t, "install", lines[len(lines)-1])
}

func TestRenderWrapper_ExportsEnvAndPath(t *testing.T) {
	got, err := RenderWrapper(WrapperInput{
		FunctionName: "check",
		Env:          map[string]string{"FOO": "bar", "WORKSPACE": "/home/devm/p"},
		PathPrepend:  []string{"/opt/x/bin", "/home/devm/go/bin"},
	})
	require.NoError(t, err)
	// Env exports are deterministic (sorted by key).
	fooIdx := strings.Index(got, `export FOO="bar"`)
	wsIdx := strings.Index(got, `export WORKSPACE="/home/devm/p"`)
	require.True(t, fooIdx >= 0 && wsIdx >= 0)
	assert.Less(t, fooIdx, wsIdx, "env keys must be sorted: FOO before WORKSPACE")
	assert.Contains(t, got, `export PATH="/opt/x/bin:/home/devm/go/bin:$PATH"`)
}

func TestRenderWrapper_CwdOptional(t *testing.T) {
	no, err := RenderWrapper(WrapperInput{FunctionName: "f"})
	require.NoError(t, err)
	assert.NotContains(t, no, "cd ")

	yes, err := RenderWrapper(WrapperInput{FunctionName: "f", Cwd: "/home/devm/main"})
	require.NoError(t, err)
	assert.Contains(t, yes, `cd "/home/devm/main"`)
}

func TestRenderWrapper_EmptyNameError(t *testing.T) {
	_, err := RenderWrapper(WrapperInput{FunctionName: ""})
	assert.Error(t, err)
}

func TestRenderWrapper_EnvValueEscaping(t *testing.T) {
	got, err := RenderWrapper(WrapperInput{
		FunctionName: "f",
		Env:          map[string]string{"MSG": `hello "world" $shell`},
	})
	require.NoError(t, err)
	// Values escape " and $ so the shell doesn't expand them.
	assert.Contains(t, got, `export MSG="hello \"world\" \$shell"`)
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		if strings.TrimSpace(ln) != "" {
			out = append(out, ln)
		}
	}
	return out
}
