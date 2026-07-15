package serviceapi

import (
	"os"

	"github.com/mdubb86/devm/internal/ironproxy"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/supervisor"
)

type ProxyStatus string

const (
	ProxyOK      ProxyStatus = "ok"
	ProxyMissing ProxyStatus = "missing"
	ProxyStale   ProxyStatus = "stale"
)

// ProxyHealth is the daemon's per-project verdict on the iron-proxy: is it
// present and current, and (if not) does healing it require the CLI to
// supply secret values the daemon can't read itself.
type ProxyHealth struct {
	Status       ProxyStatus `json:"status"`
	NeedsSecrets bool        `json:"needs_secrets"`
}

// computeProxyHealth classifies a project's iron-proxy. Lock-free snapshot
// read used only to decide; the respawn re-validates under the per-project
// lock. MISSING when no live proxy or no config file; STALE when the live
// proxy was spawned from a version stamp that differs from the current
// embedded binary; else OK. NeedsSecrets when there is drift AND the
// project injects secrets (only the CLI can resolve those).
func computeProxyHealth(sup *supervisor.Supervisor, projectID string) ProxyHealth {
	snap, _ := ReadStateSnapshot(projectID)
	needsSecrets := false
	if snap != nil {
		needsSecrets = cfgHasSecretRefs(snap.Cfg)
	}
	st := sup.Status(supervisor.Key{ProjectID: projectID, Role: supervisor.RoleProxy})
	cfgPath, _ := IronProxyConfigPath(projectID)
	_, cfgErr := os.Stat(cfgPath)
	if !st.Present || !st.Running || cfgErr != nil {
		return ProxyHealth{Status: ProxyMissing, NeedsSecrets: needsSecrets}
	}
	if snap != nil && snap.ProxyVersion != "" && snap.ProxyVersion != ironproxy.EmbeddedSha256() {
		return ProxyHealth{Status: ProxyStale, NeedsSecrets: needsSecrets}
	}
	return ProxyHealth{Status: ProxyOK, NeedsSecrets: false}
}

// cfgHasSecretRefs reports whether any env value (global or per-service) is
// a `!secret` reference — i.e. the proxy would inject secret values only
// the CLI can resolve from the keychain.
func cfgHasSecretRefs(cfg schema.Config) bool {
	for _, v := range cfg.Env {
		if v.IsSecret() {
			return true
		}
	}
	for _, svc := range cfg.Services {
		for _, v := range svc.Env {
			if v.IsSecret() {
				return true
			}
		}
	}
	return false
}
