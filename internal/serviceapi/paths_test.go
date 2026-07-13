package serviceapi

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSocketPath_ContainsAppSupport(t *testing.T) {
	got := SocketPath()
	assert.Contains(t, got, "Library/Application Support/devm/")
	assert.True(t, strings.HasSuffix(got, "devm.sock"),
		"socket path should end with devm.sock; got %q", got)
}

func TestEnsureRuntimeDir_CreatesDirectory(t *testing.T) {
	dir, err := EnsureRuntimeDir()
	require.NoError(t, err)
	assert.NotEmpty(t, dir)
	parent := filepath.Dir(SocketPath())
	assert.Equal(t, parent, dir)
}

// TestRuntimeDirEnvOverride pins that $DEVM_RUNTIME_DIR shifts every
// runtime path — socket, state dir, ensured dir — off the default
// `~/Library/Application Support/devm/` location. Load-bearing for
// e2e isolation: without this, e2e runs would still trample the
// user's real daemon state via SocketPath / StateDir.
func TestRuntimeDirEnvOverride(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("DEVM_RUNTIME_DIR", tmp)

	assert.Equal(t, tmp, RuntimeDir())
	assert.Equal(t, filepath.Join(tmp, "devm.sock"), SocketPath())
	assert.Equal(t, filepath.Join(tmp, "state"), StateDir())

	dir, err := EnsureRuntimeDir()
	require.NoError(t, err)
	assert.Equal(t, tmp, dir)
}
