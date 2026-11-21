package docker

import "github.com/mdubb86/devm/internal/schema"

// dockerImplicitAllowlist are the hosts iron-proxy must allow for a
// `docker.enabled: true` project to work end-to-end. Covers Docker Hub
// image pulls (registry-1/auth/production.cloudfront) and the apt source
// `get.docker.com`'s installer leaves behind at
// /etc/apt/sources.list.d/docker.list (download.docker.com), so every
// subsequent `apt-get update` finds it allowlisted rather than 403ing
// the whole update.
var dockerImplicitAllowlist = []string{
	"registry-1.docker.io",
	"auth.docker.io",
	"production.cloudfront.docker.com",
	"download.docker.com",
}

// EffectiveAllowlist returns the hostnames iron-proxy should allow for
// this project: every host in cfg.Network.Allow, then (if docker is
// enabled) any docker-implicit hosts not already listed. Preserves
// user-declared order; implicit hosts are appended.
func EffectiveAllowlist(cfg schema.Config) []string {
	user := cfg.Network.Domains()
	if !cfg.DockerEnabled() {
		return user
	}
	seen := make(map[string]bool, len(user))
	for _, h := range user {
		seen[h] = true
	}
	out := append([]string{}, user...)
	for _, h := range dockerImplicitAllowlist {
		if !seen[h] {
			out = append(out, h)
		}
	}
	return out
}
