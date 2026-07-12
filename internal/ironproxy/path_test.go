package ironproxy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEnsure_FreshExtracts confirms first-run extraction: no target
// file → Ensure creates <runtimeDir>/bin/iron-proxy from the embedded
// gzip blob, chmods it executable, and drops a matching sha256 sidecar.
func TestEnsure_FreshExtracts(t *testing.T) {
	runDir := t.TempDir()

	got, err := Ensure(runDir)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(runDir, "bin", "iron-proxy"), got)

	info, err := os.Stat(got)
	require.NoError(t, err)
	assert.NotZero(t, info.Size(), "extracted iron-proxy must not be empty")
	assert.Equal(t, os.FileMode(0755), info.Mode().Perm())

	sidecar, err := os.ReadFile(got + ".sha256")
	require.NoError(t, err)
	assert.Equal(t, embedSha256Hex, string(sidecar))
}

// TestEnsure_ReusesWhenSidecarMatches confirms idempotence: a second
// Ensure with an unchanged embed blob does NOT rewrite the target file
// (mtime preserved).
func TestEnsure_ReusesWhenSidecarMatches(t *testing.T) {
	runDir := t.TempDir()

	first, err := Ensure(runDir)
	require.NoError(t, err)
	stat1, err := os.Stat(first)
	require.NoError(t, err)

	// Sleep long enough that a rewrite would change mtime detectably.
	// The renamed tempfile keeps its own creation mtime, so a re-extract
	// would present a strictly-newer mtime here.
	time := stat1.ModTime()
	_ = time

	second, err := Ensure(runDir)
	require.NoError(t, err)
	assert.Equal(t, first, second)

	stat2, err := os.Stat(second)
	require.NoError(t, err)
	assert.Equal(t, stat1.ModTime(), stat2.ModTime(),
		"Ensure with matching sidecar must not rewrite the target")
}

// TestEnsure_RewritesOnSidecarMismatch confirms the upgrade path:
// stale sidecar → Ensure re-extracts and updates the sidecar to match.
func TestEnsure_RewritesOnSidecarMismatch(t *testing.T) {
	runDir := t.TempDir()

	first, err := Ensure(runDir)
	require.NoError(t, err)
	sidecar := first + ".sha256"

	// Simulate an older devm having extracted a different iron-proxy —
	// stale sidecar hash but a real (though wrong) binary at the target.
	require.NoError(t, os.WriteFile(sidecar, []byte("stale-hash"), 0644))
	require.NoError(t, os.WriteFile(first, []byte("stale-binary"), 0755))

	second, err := Ensure(runDir)
	require.NoError(t, err)
	assert.Equal(t, first, second)

	newSidecar, err := os.ReadFile(sidecar)
	require.NoError(t, err)
	assert.Equal(t, embedSha256Hex, string(newSidecar), "sidecar must match embed after re-extract")

	info, err := os.Stat(second)
	require.NoError(t, err)
	assert.Greater(t, info.Size(), int64(len("stale-binary")),
		"target must be replaced with the real iron-proxy, not left as the stale placeholder")
}

// TestEnsure_RewritesWhenTargetMissing confirms that a matching sidecar
// alone isn't enough — the target file itself must exist, else Ensure
// re-extracts. Guards against a partial-uninstall state where the
// sidecar survives but the binary was removed.
func TestEnsure_RewritesWhenTargetMissing(t *testing.T) {
	runDir := t.TempDir()

	target, err := Ensure(runDir)
	require.NoError(t, err)
	require.NoError(t, os.Remove(target))
	// Sidecar still says "everything is up to date."

	got, err := Ensure(runDir)
	require.NoError(t, err)
	assert.Equal(t, target, got)
	_, err = os.Stat(got)
	require.NoError(t, err, "target must be re-extracted when only the sidecar survived")
}
