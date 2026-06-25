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
