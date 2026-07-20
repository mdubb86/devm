package serviceapi

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mdubb86/devm/internal/identity"
)

func TestSocketPath_ContainsAppSupport(t *testing.T) {
	got := SocketPath(identity.Prod)
	assert.Contains(t, got, "Library/Application Support/devm/")
	assert.True(t, strings.HasSuffix(got, "devm.sock"),
		"socket path should end with devm.sock; got %q", got)
}

func TestEnsureRuntimeDir_CreatesDirectory(t *testing.T) {
	dir, err := EnsureRuntimeDir(identity.Prod)
	require.NoError(t, err)
	assert.NotEmpty(t, dir)
	parent := filepath.Dir(SocketPath(identity.Prod))
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

	assert.Equal(t, tmp, RuntimeDir(identity.Prod))
	assert.Equal(t, filepath.Join(tmp, "devm.sock"), SocketPath(identity.Prod))
	assert.Equal(t, filepath.Join(tmp, "state"), StateDir(identity.Prod))

	dir, err := EnsureRuntimeDir(identity.Prod)
	require.NoError(t, err)
	assert.Equal(t, tmp, dir)
}
