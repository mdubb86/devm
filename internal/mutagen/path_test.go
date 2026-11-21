package mutagen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsure_ExtractsBinaryAndSidecar(t *testing.T) {
	dir := t.TempDir()
	got, err := Ensure(dir)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "bin", "mutagen"), got)

	info, err := os.Stat(got)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0755), info.Mode().Perm())

	sidecar, err := os.ReadFile(filepath.Join(dir, "bin", "mutagen.sha256"))
	require.NoError(t, err)
	assert.Equal(t, EmbeddedSha256(), string(sidecar))
}

func TestEnsure_IsIdempotentOnShaMatch(t *testing.T) {
	dir := t.TempDir()
	_, err := Ensure(dir)
	require.NoError(t, err)
	first, err := os.Stat(filepath.Join(dir, "bin", "mutagen"))
	require.NoError(t, err)

	_, err = Ensure(dir)
	require.NoError(t, err)
	second, err := os.Stat(filepath.Join(dir, "bin", "mutagen"))
	require.NoError(t, err)

	// idempotent: no re-extract => same mtime (nanoseconds should match)
	assert.Equal(t, first.ModTime().UnixNano(), second.ModTime().UnixNano())
}

func TestEnsure_ReExtractsOnShaMismatch(t *testing.T) {
	dir := t.TempDir()
	_, err := Ensure(dir)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bin", "mutagen.sha256"), []byte("garbage"), 0644))

	_, err = Ensure(dir)
	require.NoError(t, err)
	sidecar, err := os.ReadFile(filepath.Join(dir, "bin", "mutagen.sha256"))
	require.NoError(t, err)
	assert.Equal(t, EmbeddedSha256(), string(sidecar))
}
