package serviceapi

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/secret"
)

// ResolveSecretBindings gathers every `!secret <name>` ref from cfg
// (top-level env + per-service env, deduped) plus every repo secret
// (`repo.secret` at top level and `volumes.<name>.repo.secret`,
// inherited or own), looks each up in the on-disk secret store under
// "<project>/<name>", and attaches the injection-host scope: for env
// secrets, the hosts declared in network.allow; for repo secrets, the
// host of the repo's own clone URL — so a bare top-level `repo.secret:`
// is sufficient, with no matching network.allow entry required.
// Returns the bindings a caller hands to iron-proxy. A secret with no
// host scope at all is still resolved and sent with empty Hosts
// (iron-proxy omits it — never injects).
//
// macCwd resolves the top-level repo's clone URL when repo.url is nil
// (`git remote get-url origin` in macCwd). Volumes always declare url
// explicitly (schema-enforced), so macCwd is never consulted for them.
//
// Lives in serviceapi (not orchestrator) so the daemon can call it
// directly against the on-disk file-store backend — the daemon
// cannot import orchestrator (import cycle: orchestrator already
// imports serviceapi).
func ResolveSecretBindings(cfg schema.Config, backend secret.Backend, macCwd string) ([]SecretBinding, error) {
	seen := map[string]bool{}
	var names []string
	collect := func(env map[string]schema.EnvValue) {
		for _, v := range env {
			if v.Secret != nil && !seen[v.Secret.Name] {
				seen[v.Secret.Name] = true
				names = append(names, v.Secret.Name)
			}
		}
	}
	collect(cfg.Env)
	for _, svc := range cfg.Services {
		collect(svc.Env)
	}

	repoHostsBySecret, _, err := repoSecretHosts(cfg, macCwd)
	if err != nil {
		return nil, err
	}
	for name := range repoHostsBySecret {
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}

	sort.Strings(names)
	if len(names) == 0 {
		return nil, nil
	}

	hosts := cfg.Network.SecretHosts()
	for name, repoHosts := range repoHostsBySecret {
		hosts[name] = mergeSortedUnique(hosts[name], repoHosts)
	}

	var bindings []SecretBinding
	var missing []string
	for _, n := range names {
		v, err := backend.Get(cfg.Project.Name + "/" + n)
		if err != nil {
			missing = append(missing, n)
			continue
		}
		bindings = append(bindings, SecretBinding{Name: n, Value: v, Hosts: hosts[n]})
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing secrets: %v (set with `devm secret set <name>`)", missing)
	}
	return bindings, nil
}

// RepoHosts returns the sorted, de-duplicated hosts implied by cfg's
// top-level `repo:` declaration — the egress-allowlist entries that
// clone needs beyond whatever network.allow already declares. macCwd
// resolves the repo's URL when nil, exactly as ResolveSecretBindings
// does.
func RepoHosts(cfg schema.Config, macCwd string) ([]string, error) {
	_, hosts, err := repoSecretHosts(cfg, macCwd)
	return hosts, err
}

// repoSecretHosts walks cfg's repo declarations and returns both the
// secret-name -> hosts map (for ResolveSecretBindings' injection scope)
// and the flat, sorted, de-duplicated host list (for RepoHosts' egress
// scope). Every repo declaration schema-validates to a non-empty
// effective secret, so the two views cover the same set of hosts.
func repoSecretHosts(cfg schema.Config, macCwd string) (map[string][]string, []string, error) {
	bySecret := map[string]map[string]bool{}
	seenHost := map[string]bool{}
	var hosts []string

	add := func(secretName, rawURL string) error {
		if secretName == "" || rawURL == "" {
			return nil
		}
		host, err := repoURLHost(rawURL)
		if err != nil {
			return err
		}
		if host == "" {
			// URL schemes with no authority component (file://, git://
			// with local path) never route through iron-proxy — nothing
			// to inject or allowlist for them.
			return nil
		}
		if bySecret[secretName] == nil {
			bySecret[secretName] = map[string]bool{}
		}
		bySecret[secretName][host] = true
		if !seenHost[host] {
			seenHost[host] = true
			hosts = append(hosts, host)
		}
		return nil
	}

	// TODO(Task 17): walk cfg.Repos, deriving URL from macCwd via
	// repohelpers.DeriveRepoURL when a given entry's URL is nil, and
	// call add(secret, url) for each. No-op until then.
	_ = add

	out := make(map[string][]string, len(bySecret))
	for s, hostSet := range bySecret {
		list := make([]string, 0, len(hostSet))
		for h := range hostSet {
			list = append(list, h)
		}
		sort.Strings(list)
		out[s] = list
	}
	sort.Strings(hosts)
	return out, hosts, nil
}

// repoURLHost extracts the hostname from a git clone URL. Handles both
// standard URLs (https://host/org/repo.git, ssh://host/...) via
// url.Parse, and scp-like syntax (git@host:org/repo.git) which
// url.Parse rejects (the colon before any slash reads as an invalid
// scheme) via a manual fallback split.
func repoURLHost(rawURL string) (string, error) {
	if u, err := url.Parse(rawURL); err == nil {
		if u.Host == "" {
			// file:// (and similar host-less schemes) parse cleanly with
			// no Host — no host implied, not the scp-like fallback below.
			return "", nil
		}
		return u.Hostname(), nil
	}
	rest := rawURL
	if i := strings.Index(rest, "@"); i >= 0 {
		rest = rest[i+1:]
	}
	if i := strings.Index(rest, ":"); i >= 0 && rest[:i] != "" {
		return rest[:i], nil
	}
	return "", fmt.Errorf("cannot determine host from repo url %q", rawURL)
}

// AppendUniqueHosts returns base with any of extra's entries not
// already present appended, in extra's order. Unlike
// mergeSortedUnique, it does not sort — callers merging repo hosts
// into an egress allowlist need the user-declared/Docker-Hub order
// EffectiveAllowlist already establishes preserved, not alphabetized.
func AppendUniqueHosts(base, extra []string) []string {
	seen := make(map[string]bool, len(base)+len(extra))
	out := make([]string, 0, len(base)+len(extra))
	for _, h := range base {
		if !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	for _, h := range extra {
		if !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	return out
}

// mergeSortedUnique unions a and b into a sorted, de-duplicated slice.
func mergeSortedUnique(a, b []string) []string {
	if len(a) == 0 {
		out := append([]string{}, b...)
		sort.Strings(out)
		return out
	}
	if len(b) == 0 {
		out := append([]string{}, a...)
		sort.Strings(out)
		return out
	}
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, h := range a {
		if !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	for _, h := range b {
		if !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	sort.Strings(out)
	return out
}
