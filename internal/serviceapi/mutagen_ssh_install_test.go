package serviceapi

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"github.com/mdubb86/devm/internal/tartmutagenssh"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureTartMutagenSSH_WritesBothNames(t *testing.T) {
	cfg := testMutagenCfg(t)

	sshDir, err := EnsureTartMutagenSSH(cfg)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(cfg.RuntimeDir(), "mutagen-ssh-dir"), sshDir)

	sshPath := filepath.Join(sshDir, "ssh")
	scpPath := filepath.Join(sshDir, "scp")

	sshBytes, err := os.ReadFile(sshPath)
	require.NoError(t, err)
	scpBytes, err := os.ReadFile(scpPath)
	require.NoError(t, err)

	want := tartmutagenssh.Bytes()
	assert.Equal(t, sha256.Sum256(want), sha256.Sum256(sshBytes),
		"ssh must match embedded shim bytes")
	assert.Equal(t, sha256.Sum256(want), sha256.Sum256(scpBytes),
		"scp must match embedded shim bytes")

	sshInfo, err := os.Stat(sshPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), sshInfo.Mode().Perm(),
		"ssh must be executable")
	scpInfo, err := os.Stat(scpPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), scpInfo.Mode().Perm(),
		"scp must be executable")
}

func TestEnsureTartMutagenSSH_Idempotent(t *testing.T) {
	cfg := testMutagenCfg(t)

	sshDir, err := EnsureTartMutagenSSH(cfg)
	require.NoError(t, err)
	stat1, err := os.Stat(filepath.Join(sshDir, "ssh"))
	require.NoError(t, err)

	// Call again — should be a no-op if bytes unchanged.
	_, err = EnsureTartMutagenSSH(cfg)
	require.NoError(t, err)
	stat2, err := os.Stat(filepath.Join(sshDir, "ssh"))
	require.NoError(t, err)

	assert.Equal(t, stat1.ModTime(), stat2.ModTime(),
		"idempotent Ensure should not rewrite unchanged file")
}
