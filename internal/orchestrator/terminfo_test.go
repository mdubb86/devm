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
