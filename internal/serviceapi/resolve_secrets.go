package serviceapi

import (
	"fmt"
	"sort"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/secret"
)

// ResolveSecretBindings gathers every `!secret <name>` ref from cfg
// (top-level env + per-service env, deduped), looks each up in the
// on-disk secret store under "<project>/<name>", and attaches the
// injection-host scope declared in network.allow. Returns the
// bindings a caller hands to iron-proxy. A secret with no
// network.allow host scope is still resolved and sent with empty
// Hosts (iron-proxy omits it — never injects).
//
// Lives in serviceapi (not orchestrator) so the daemon can call it
// directly against the on-disk file-store backend — the daemon
// cannot import orchestrator (import cycle: orchestrator already
// imports serviceapi).
func ResolveSecretBindings(cfg schema.Config, backend secret.Backend) ([]SecretBinding, error) {
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
	sort.Strings(names)
	if len(names) == 0 {
		return nil, nil
	}

	hosts := cfg.Network.SecretHosts()
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
