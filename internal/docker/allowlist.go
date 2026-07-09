package docker

import "github.com/mdubb86/devm/internal/schema"

// dockerHubHosts are the minimum set of hosts required for
// `docker pull` against Docker's default registry to succeed. Added
// implicitly to iron-proxy's allowlist when Config.Docker == true.
var dockerHubHosts = []string{
	"registry-1.docker.io",
	"auth.docker.io",
	"production.cloudfront.docker.com",
}

// EffectiveAllowlist returns the hostnames iron-proxy should allow for
// this project: every host in cfg.Network.Allow, then (if
// cfg.Docker == true) any Docker Hub hosts not already listed.
// Preserves user-declared order; docker-hub hosts are appended.
func EffectiveAllowlist(cfg schema.Config) []string {
	user := cfg.Network.Domains()
	if !cfg.Docker {
		return user
	}
	seen := make(map[string]bool, len(user))
	for _, h := range user {
		seen[h] = true
	}
	out := append([]string{}, user...)
	for _, h := range dockerHubHosts {
		if !seen[h] {
			out = append(out, h)
		}
	}
	return out
}
