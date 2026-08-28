package serviceapi

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// CloneRequest describes a fresh, in-guest git clone: where to fetch
// from, which secret to authenticate with, where in the guest the
// clone should land, and the iron-proxy/CA plumbing needed to reach
// the URL through the sandboxed egress path.
type CloneRequest struct {
	URL             string
	SecretName      string
	GuestTargetPath string
	IronProxyURL    string
	GuestCACertPath string
}

// CloneRepoInGuest runs `git clone` inside the guest as the devm user,
// routed through iron-proxy so the secret named by req.SecretName is
// substituted onto the wire without this process ever holding its
// plaintext value. The __DEVM_SECRET_<name>__ placeholder is injected
// via a Basic-auth http.extraheader; iron-proxy decodes it, swaps in
// the resolved secret, and re-encodes before forwarding upstream.
//
// GuestCACertPath must name the guest's trust store entry for
// iron-proxy's MITM root cert — without it git rejects the proxy's
// CONNECT tunnel and every proxied clone fails closed.
//
// Fails loud: a transport error or nonzero exit returns an error that
// names the git-clone step and carries stderr.
func CloneRepoInGuest(exec GuestExec, req CloneRequest) error {
	authRaw := "x-access-token:" + tokenPlaceholderFor(req.SecretName)
	authB64 := base64.StdEncoding.EncodeToString([]byte(authRaw))
	extraHeader := "Authorization: Basic " + authB64

	// `-c http.proxy=` forces git to route through iron-proxy even when
	// the URL host resolves to loopback (git's default HTTP_PROXY handling
	// bypasses loopback). Redundant for typical remote URLs where the env
	// var already applies; required for URLs like http://127.0.0.1:PORT/.
	script := fmt.Sprintf(`sudo -u devm bash -c %s`, PosixShellQuote(fmt.Sprintf(`set -e
export HTTP_PROXY=%s
export HTTPS_PROXY=%s
export GIT_SSL_CAINFO=%s
git clone --quiet -c %s -c %s %s %s
`,
		req.IronProxyURL,
		req.IronProxyURL,
		req.GuestCACertPath,
		PosixShellQuote("http.proxy="+req.IronProxyURL),
		PosixShellQuote("http.extraheader="+extraHeader),
		PosixShellQuote(req.URL),
		PosixShellQuote(req.GuestTargetPath),
	)))

	stdout, stderr, exitCode, err := exec(script)
	if err != nil {
		return fmt.Errorf("mutagen cold start: git clone %s: %w", req.URL, err)
	}
	if exitCode != 0 {
		return fmt.Errorf("mutagen cold start: git clone %s: exit %d: %s",
			req.URL, exitCode, strings.TrimSpace(stderr+stdout))
	}
	return nil
}

// tokenPlaceholderFor returns the __DEVM_SECRET_<name>__ placeholder
// iron-proxy's secrets transform substitutes on the wire.
func tokenPlaceholderFor(secretName string) string {
	return "__DEVM_SECRET_" + secretName + "__"
}
