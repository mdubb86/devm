package serviceapi

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCanonicalResolverContents_IsStable(t *testing.T) {
	got := canonicalResolverContents()
	assert.Equal(t, "nameserver 127.0.0.1\nport 51153\n", got,
		"canonical contents changed — coordinate with any pinned consumers")
}

func TestCheckResolverFileAt_Missing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test")
	state, err := checkResolverFileAt(path)
	require.NoError(t, err)
	assert.Equal(t, ResolverFileMissing, state)
}

func TestCheckResolverFileAt_Matches(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test")
	require.NoError(t, os.WriteFile(path, []byte(canonicalResolverContents()), 0644))
	state, err := checkResolverFileAt(path)
	require.NoError(t, err)
	assert.Equal(t, ResolverFileMatches, state)
}

func TestCheckResolverFileAt_Diverged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test")
	require.NoError(t, os.WriteFile(path, []byte("nameserver 8.8.8.8\n"), 0644))
	state, err := checkResolverFileAt(path)
	require.NoError(t, err)
	assert.Equal(t, ResolverFileDiverged, state)
}
