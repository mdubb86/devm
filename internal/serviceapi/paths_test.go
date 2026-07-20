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
	got := identity.Prod.SocketPath()
	assert.Contains(t, got, "Library/Application Support/devm/")
	assert.True(t, strings.HasSuffix(got, "devm.sock"),
		"socket path should end with devm.sock; got %q", got)
}

func TestEnsureRuntimeDir_CreatesDirectory(t *testing.T) {
	dir, err := EnsureRuntimeDir(identity.Prod)
	require.NoError(t, err)
	assert.NotEmpty(t, dir)
	parent := filepath.Dir(identity.Prod.SocketPath())
	assert.Equal(t, parent, dir)
}
