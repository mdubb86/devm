package orchestrator

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCaptureHostTerminfo_EmptyTermReturnsEmpty(t *testing.T) {
	t.Setenv("TERM", "")
	assert.Equal(t, "", captureHostTerminfo())
}

func TestCaptureHostTerminfo_UnknownTermReturnsEmpty(t *testing.T) {
	// A truly unknown TERM string — infocmp will error.
	t.Setenv("TERM", "this-terminal-does-not-exist-xyzzy")
	assert.Equal(t, "", captureHostTerminfo())
}

func TestTerminalEnvForward_IncludesSetHostTermVars(t *testing.T) {
	t.Setenv("TERM", "xterm-ghostty")
	t.Setenv("COLORTERM", "truecolor")
	t.Setenv("LANG", "en_US.UTF-8")
	// Explicitly clear the others so the assertions don't depend on
	// whatever the test host happens to have exported.
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_CTYPE", "")

	got := terminalEnvForward()
	require.NotNil(t, got, "expected env(1) prefix argv, got nil")
	assert.Equal(t, "env", got[0])
	assert.Contains(t, got, "TERM=xterm-ghostty")
	assert.Contains(t, got, "COLORTERM=truecolor")
	assert.Contains(t, got, "LANG=en_US.UTF-8")
	// Empty-value vars must be dropped, not emitted as "LC_ALL=".
	for _, s := range got {
		assert.NotEqual(t, "LC_ALL=", s)
		assert.NotEqual(t, "LC_CTYPE=", s)
	}
}

func TestTerminalEnvForward_ReturnsNilWhenNothingSet(t *testing.T) {
	// All the forwarded vars empty AND infocmp will fail (no TERM).
	t.Setenv("TERM", "")
	t.Setenv("COLORTERM", "")
	t.Setenv("LANG", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_CTYPE", "")
	assert.Nil(t, terminalEnvForward(),
		"nothing forwarded → return nil so the caller skips the env(1) wrap")
}

func TestCaptureHostTerminfo_KnownTermReturnsBase64Source(t *testing.T) {
	// xterm-256color is on essentially every machine that has infocmp.
	// If this fails, it means infocmp is missing — t.Skip in that case
	// rather than fail.
	t.Setenv("TERM", "xterm-256color")
	got := captureHostTerminfo()
	if got == "" {
		t.Skip("host has no infocmp / no xterm-256color entry — can't exercise")
	}
	decoded, err := base64.StdEncoding.DecodeString(got)
	require.NoError(t, err, "captured blob must decode as base64")
	src := string(decoded)
	// Real infocmp output starts with a comment line, then the terminal
	// name line. Confirm we got terminfo source shape.
	assert.True(t, strings.Contains(src, "xterm-256color"),
		"decoded blob must mention the terminal name: %q", src)
}
