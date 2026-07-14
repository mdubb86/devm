package sshkeys

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestEnsureProjectKeypair_GeneratesOnFirstCall(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", filepath.Join(t.TempDir(), "rd"))
	pub, err := EnsureProjectKeypair("myproj")
	require.NoError(t, err)
	require.NotEmpty(t, pub)

	// Files exist with expected modes.
	dir := ProjectDir("myproj")
	priv, err := os.Stat(filepath.Join(dir, "id_ed25519"))
	require.NoError(t, err)
	assert.EqualValues(t, 0o600, priv.Mode().Perm())

	pubStat, err := os.Stat(filepath.Join(dir, "id_ed25519.pub"))
	require.NoError(t, err)
	assert.EqualValues(t, 0o644, pubStat.Mode().Perm())

	// Pubkey is an OpenSSH-format ed25519 line.
	assert.True(t, strings.HasPrefix(string(pub), "ssh-ed25519 "),
		"pub should start with 'ssh-ed25519 ', got %q", string(pub))
}

func TestEnsureProjectKeypair_IdempotentOnSecondCall(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", filepath.Join(t.TempDir(), "rd"))
	first, err := EnsureProjectKeypair("p")
	require.NoError(t, err)
	second, err := EnsureProjectKeypair("p")
	require.NoError(t, err)
	assert.Equal(t, string(first), string(second),
		"second call must return the same pubkey — no regeneration")
}

func TestEnsureProjectHostKey_WritesKnownHostsLine(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", filepath.Join(t.TempDir(), "rd"))
	priv, pub, err := EnsureProjectHostKey("p", "p-vm")
	require.NoError(t, err)
	require.NotEmpty(t, priv)
	require.NotEmpty(t, pub)

	// known_hosts is one line: "devm-<vm-name> ssh-ed25519 <base64>"
	kh, err := os.ReadFile(filepath.Join(ProjectDir("p"), "known_hosts"))
	require.NoError(t, err)
	line := strings.TrimSpace(string(kh))
	assert.True(t, strings.HasPrefix(line, "devm-p-vm ssh-ed25519 "),
		"known_hosts prefix mismatch: %q", line)

	// Pubkey parses.
	_, _, _, _, err = ssh.ParseAuthorizedKey([]byte(line))
	require.NoError(t, err, "known_hosts line must be parseable as authorized key")
}

func TestEnsureProjectHostKey_Idempotent(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", filepath.Join(t.TempDir(), "rd"))
	priv1, pub1, err := EnsureProjectHostKey("p", "p-vm")
	require.NoError(t, err)
	priv2, pub2, err := EnsureProjectHostKey("p", "p-vm")
	require.NoError(t, err)
	assert.Equal(t, priv1, priv2)
	assert.Equal(t, pub1, pub2)
}

func TestRemove_Idempotent(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", filepath.Join(t.TempDir(), "rd"))
	require.NoError(t, Remove("nope"))
	_, err := EnsureProjectKeypair("p")
	require.NoError(t, err)
	require.NoError(t, Remove("p"))
	_, err = os.Stat(ProjectDir("p"))
	assert.True(t, os.IsNotExist(err), "project dir must be gone after Remove")
}

func TestEnsureProjectKeypair_RejectsPathTraversal(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", filepath.Join(t.TempDir(), "rd"))
	for _, id := range []string{"../evil", "foo/bar", "..", "foo\\bar"} {
		t.Run(id, func(t *testing.T) {
			_, err := EnsureProjectKeypair(id)
			require.Error(t, err)
		})
	}
}
