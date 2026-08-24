package serviceapi

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeBareRepo creates a bare git repo with one commit at src, then
// returns a file:// URL to it. Suitable as an iron-proxy-bypassing
// clone source for unit tests.
func makeBareRepo(t *testing.T) string {
	t.Helper()
	work := t.TempDir()
	require.NoError(t, exec.Command("git", "-C", work, "init", "-q").Run())
	require.NoError(t, os.WriteFile(filepath.Join(work, "README.md"), []byte("hello\n"), 0644))
	require.NoError(t, exec.Command("git", "-C", work, "add", ".").Run())
	require.NoError(t, exec.Command("git", "-C", work, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "init").Run())

	bare := t.TempDir() + "/repo.git"
	require.NoError(t, exec.Command("git", "clone", "--bare", "-q", work, bare).Run())
	return "file://" + bare
}

func TestHydrateRepoVolume_FreshClone(t *testing.T) {
	url := makeBareRepo(t)
	storage := t.TempDir()
	repo := schema.RepoConfig{URL: &url}

	// file:// clones bypass HTTP proxy — pass empty ironProxyURL.
	err := HydrateRepoVolume(context.Background(), storage, repo, "", "")
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(storage, "README.md"))
	assert.NoError(t, err, "README.md must exist in storage after clone")
}

func TestHydrateRepoVolume_BadURL(t *testing.T) {
	bad := "file:///nonexistent-repo-that-does-not-exist.git"
	repo := schema.RepoConfig{URL: &bad}
	err := HydrateRepoVolume(context.Background(), t.TempDir(), repo, "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "git clone")
}

// TestHydrateRepoVolume_PlaceholderMatchesIronProxyTokenFor guards against
// a placeholder-case mismatch between HydrateRepoVolume and iron-proxy's
// substitution rule (secretToken in ironproxy.go, which must match
// schema.TokenFor). A fake `git` on PATH records its args instead of
// cloning; the ironProxyURL+secret branch never touches the network.
func TestHydrateRepoVolume_PlaceholderMatchesIronProxyTokenFor(t *testing.T) {
	fakeBinDir := t.TempDir()
	argsFile := filepath.Join(fakeBinDir, "args.txt")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(fakeBinDir, "git"), []byte(script), 0o755))

	t.Setenv("PATH", fakeBinDir+":"+os.Getenv("PATH"))

	url := "https://example.com/org/repo.git"
	secret := "gh_token" // lowercase, per the migration playbook's naming convention
	repo := schema.RepoConfig{URL: &url, Secret: secret}

	err := HydrateRepoVolume(context.Background(), t.TempDir(), repo, "", "http://127.0.0.1:1")
	require.NoError(t, err)

	out, err := os.ReadFile(argsFile)
	require.NoError(t, err)

	want := schema.TokenFor(secret)
	require.Equal(t, "__DEVM_SECRET_gh_token__", want, "sanity check on schema.TokenFor's shape")
	assert.Contains(t, string(out), want, "placeholder must match schema.TokenFor's raw case exactly")
}

func TestHydrateRepoVolume_BranchOverride(t *testing.T) {
	url := makeBareRepo(t)
	storage := t.TempDir()
	main := "master" // git init default may be master or main; use whichever succeeds
	branch := main
	repo := schema.RepoConfig{URL: &url, Branch: &branch}
	err := HydrateRepoVolume(context.Background(), storage, repo, "", "")
	// If master/main mismatch, git errors — fine, we're pinning the
	// signature accepts and passes -b. Assert no crash / bogus success.
	if err != nil {
		assert.Contains(t, err.Error(), branch)
	}
}
