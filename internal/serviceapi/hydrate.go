package serviceapi

import (
	"context"
	"encoding/base64"
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
// caCertPath points at iron-proxy's MITM root cert (IronProxyConfig.CACertPath
// on the host filesystem). This git clone runs on the host, not the guest —
// the guest's trust of that same CA comes from update-ca-certificates into
// its system store (caenv.Vars), which this host process never goes through.
// Without GIT_SSL_CAINFO naming it explicitly, git rejects iron-proxy's MITM
// certificate for the CONNECT tunnel and the clone fails closed.
//
// Fails loud: any nonzero git exit wraps stderr into the returned error.
// storagePath must be empty; the caller ensures this via
// ensureVolumeMacDir's wasEmpty return.
func HydrateRepoVolume(ctx context.Context, storagePath string, repo schema.RepoConfig, inheritedSecret, ironProxyURL, caCertPath string) error {
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
		args = append(args, "-c",
			"http.extraheader="+hydrateExtraHeader(secret))
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
		if caCertPath != "" {
			env = append(env, "GIT_SSL_CAINFO="+caCertPath)
		}
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git clone %s: %s: %w", *repo.URL, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// hydrateExtraHeader returns the Authorization header string hydrate
// injects via `git -c http.extraheader=<...>`. Split out so unit tests
// can pin the exact wire shape without executing git.
//
// Shape: `Authorization: Basic base64("x-access-token:<placeholder>")`.
// Iron-proxy's secrets transform decodes the Basic payload, replaces
// the placeholder with the resolved secret value, and re-encodes.
// GitHub / GitLab / Azure DevOps all accept this shape uniformly for
// git-over-HTTPS operations; bearer works only for narrower
// provider+token combinations.
func hydrateExtraHeader(secretName string) string {
	placeholder := schema.TokenFor(secretName)
	blob := base64.StdEncoding.EncodeToString(
		[]byte("x-access-token:" + placeholder))
	return "Authorization: Basic " + blob
}
