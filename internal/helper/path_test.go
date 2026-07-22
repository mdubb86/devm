package helper

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtract_WritesBinaryAndSidecar(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "devm-helper")

	got, err := Extract(target)
	require.NoError(t, err)
	assert.Equal(t, target, got)

	fi, err := os.Stat(target)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0755), fi.Mode().Perm(), "target must be executable")
	assert.Greater(t, fi.Size(), int64(0), "target must be non-empty")

	sidecar, err := os.ReadFile(target + ".sha256")
	require.NoError(t, err)
	assert.Equal(t, EmbeddedSha256(), string(sidecar), "sidecar must record the embed sha256")
}

func TestExtract_IdempotentOnMatchingSidecar(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "devm-helper")

	_, err := Extract(target)
	require.NoError(t, err)

	// Corrupt the binary but leave the sidecar valid → Extract should
	// notice the sidecar matches embed and NOT re-extract, so the
	// corrupted bytes stay in place.
	require.NoError(t, os.WriteFile(target, []byte("corrupted"), 0755))

	_, err = Extract(target)
	require.NoError(t, err)

	got, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, []byte("corrupted"), got, "matching sidecar should short-circuit re-extraction")
}

func TestExtract_ReExtractsWhenSidecarMissing(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "devm-helper")

	_, err := Extract(target)
	require.NoError(t, err)

	// Delete the sidecar → next Extract must re-decompress.
	require.NoError(t, os.Remove(target+".sha256"))
	require.NoError(t, os.WriteFile(target, []byte("stale"), 0755))

	_, err = Extract(target)
	require.NoError(t, err)

	got, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.NotEqual(t, []byte("stale"), got, "missing sidecar should force re-extraction")

	// Fresh extract's sha256 must match EmbeddedSha256 after decompression.
	// We hash the on-disk binary after gzipping to compare — but easier:
	// just verify sidecar was rewritten.
	sidecar, err := os.ReadFile(target + ".sha256")
	require.NoError(t, err)
	assert.Equal(t, EmbeddedSha256(), string(sidecar))
}

func TestEmbeddedSha256_MatchesBlobHash(t *testing.T) {
	// EmbeddedSha256 must be the sha256 of the raw embedded gzip bytes.
	// If they diverge, the sidecar-idempotency logic breaks.
	h := sha256.Sum256(devmHelperGz)
	assert.Equal(t, hex.EncodeToString(h[:]), EmbeddedSha256())
}
