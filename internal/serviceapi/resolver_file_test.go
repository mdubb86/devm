package serviceapi

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mdubb86/devm/internal/identity"
)

func TestCheckResolverFileAt_Missing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test")
	state, err := checkResolverFileAt(identity.Prod, path)
	require.NoError(t, err)
	assert.Equal(t, ResolverFileMissing, state)
}

func TestCheckResolverFileAt_Matches(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test")
	require.NoError(t, os.WriteFile(path, []byte(identity.Prod.CanonicalResolverContents()), 0644))
	state, err := checkResolverFileAt(identity.Prod, path)
	require.NoError(t, err)
	assert.Equal(t, ResolverFileMatches, state)
}

func TestCheckResolverFileAt_Diverged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test")
	require.NoError(t, os.WriteFile(path, []byte("nameserver 8.8.8.8\n"), 0644))
	state, err := checkResolverFileAt(identity.Prod, path)
	require.NoError(t, err)
	assert.Equal(t, ResolverFileDiverged, state)
}
