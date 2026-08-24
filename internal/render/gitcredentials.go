package render

import (
	"strings"

	"github.com/mdubb86/devm/internal/schema"
)

// RepoBinding is one flat, pre-resolved per-repo entry the credential
// renderer consumes. The caller (internal/provision) assembles the
// slice from schema.Config's top-level Repo and per-volume Repo
// blocks, with the primary URL already resolved (nil URLs turned into
// the git-remote-origin value at cold-start).
type RepoBinding struct {
	// URL is the resolved absolute repo URL (must be scheme://host/path;
	// the renderer injects x-access-token:<placeholder>@ between scheme
	// and host verbatim).
	URL string
	// Secret is the devm secret name whose value iron-proxy substitutes
	// at request time.
	Secret string
}

// RenderGitCredentials returns the bodies for /home/devm/.git-credentials
// and /home/devm/.gitconfig derived from bindings. Pure: no I/O.
// Empty bindings → empty credentials file, fixed gitconfig body.
//
// Line ordering follows bindings order — the caller is responsible for
// producing a deterministic order (primary first, then secondaries in
// map-key sort order) so the file diffs cleanly across re-emits.
func RenderGitCredentials(bindings []RepoBinding) (credentials, gitconfig string) {
	var b strings.Builder
	for _, r := range bindings {
		// Insert x-access-token:<placeholder>@ between the scheme and
		// the host. Substring-based (not url.Parse) because we want the
		// URL to reach the guest byte-for-byte as declared, minus the
		// userinfo we're adding.
		idx := strings.Index(r.URL, "://")
		if idx < 0 {
			continue // malformed URL — skip; validation lives elsewhere
		}
		scheme := r.URL[:idx+3]
		rest := r.URL[idx+3:]
		b.WriteString(scheme)
		b.WriteString("x-access-token:")
		b.WriteString(schema.TokenFor(r.Secret))
		b.WriteByte('@')
		b.WriteString(rest)
		b.WriteByte('\n')
	}
	return b.String(), fixedGitconfigBody()
}

// fixedGitconfigBody is the entire body of the devm-managed
// /home/devm/.gitconfig. useHttpPath=true routes same-host
// different-secret repos by full URL including path. Emitted
// unconditionally so multi-repo growth needs no gitconfig regeneration.
func fixedGitconfigBody() string {
	return "[credential]\n    helper = store\n    useHttpPath = true\n"
}
