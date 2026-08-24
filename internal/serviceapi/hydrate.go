package serviceapi

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/mdubb86/devm/internal/schema"
)

// HydrateRepoVolume runs `git clone` into storagePath. Uses inheritedSecret
// when repo.Secret is empty. Sets HTTP{,S}_PROXY to ironProxyURL when
// non-empty, and injects an Authorization http.extraheader carrying the
// __DEVM_SECRET_<name>__ placeholder that iron-proxy substitutes on the
// wire. Empty ironProxyURL (unit tests, file:// URLs) skips the proxy and
// header — the caller is responsible for whatever auth the URL scheme
// needs.
//
// Fails loud: any nonzero git exit wraps stderr into the returned error.
// storagePath must be empty; the caller ensures this via
// ensureVolumeMacDir's wasEmpty return.
func HydrateRepoVolume(ctx context.Context, storagePath string, repo schema.RepoConfig, inheritedSecret, ironProxyURL string) error {
	if repo.URL == nil || *repo.URL == "" {
		return fmt.Errorf("repo.url is required for hydration")
	}
	secret := repo.Secret
	if secret == "" {
		secret = inheritedSecret
	}
	args := []string{"clone", "--quiet"}
	if repo.Branch != nil && *repo.Branch != "" {
		args = append(args, "-b", *repo.Branch)
	}
	if ironProxyURL != "" && secret != "" {
		placeholder := schema.TokenFor(secret)
		args = append(args,
			"-c", fmt.Sprintf("http.extraheader=Authorization: bearer %s", placeholder))
	}
	args = append(args, *repo.URL, storagePath)

	cmd := exec.CommandContext(ctx, "git", args...)
	env := os.Environ()
	// Gated on ironProxyURL alone (not && secret != ""): schema.Validate()
	// guarantees a validated RepoConfig always resolves a non-empty secret
	// (own or inherited), so ironProxyURL != "" with an empty secret can't
	// occur on a real cold-start.
	if ironProxyURL != "" {
		env = append(env, "HTTP_PROXY="+ironProxyURL, "HTTPS_PROXY="+ironProxyURL)
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git clone %s: %s: %w", *repo.URL, strings.TrimSpace(string(out)), err)
	}
	return nil
}
