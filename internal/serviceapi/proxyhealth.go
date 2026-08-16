package serviceapi

import (
	"os"

	"github.com/mdubb86/devm/internal/identity"
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

// ProxyHealth is the daemon's per-project verdict on the iron-proxy: is
// it present and current, and does the project's config bind any
// host-scoped secrets (NeedsSecrets — informational; consumed by the
// CLI status renderer, format.go). Rebind reports the most recent
// outcome of the daemon-startup rebind pass for this project's
// :80/:443 listeners — nil when no rebind was attempted.
type ProxyHealth struct {
	Status       ProxyStatus   `json:"status"`
	NeedsSecrets bool          `json:"needs_secrets"`
	Rebind       *RebindReport `json:"rebind,omitempty"`
}

// RebindReport mirrors the ProxyServer's per-project RebindStatus for
// external consumers (CLI status renderer, tests). Omitted from JSON
// when nil.
type RebindReport struct {
	State     RebindState `json:"state"`
	Attempts  int         `json:"attempts"`
	LastError string      `json:"last_error,omitempty"`
}

// computeProxyHealth classifies a project's iron-proxy. Lock-free snapshot
// read used only to decide; the respawn re-validates under the per-project
// lock. MISSING when no live proxy or no config file; STALE when the live
// proxy was spawned from a version stamp that differs from the current
// embedded binary; else OK. NeedsSecrets when there is drift AND the
// project injects secrets — purely informational (healing itself doesn't
// depend on it; rebuildIronProxyConfig resolves secret values from the
// on-disk store regardless). proxy is consulted for the most recent
// startup-rebind outcome; nil is tolerated (some callers don't have a
// *ProxyServer in scope).
func computeProxyHealth(cfg identity.Config, sup *supervisor.Supervisor, proxy *ProxyServer, projectID string) ProxyHealth {
	snap, _ := ReadStateSnapshot(cfg, projectID)
	needsSecrets := false
	if snap != nil {
		needsSecrets = cfgHasSecretRefs(snap.Cfg)
	}

	// Attach rebind report if one exists — nil when no rebind was
	// attempted (e.g. project wasn't running at daemon startup).
	var rebind *RebindReport
	if proxy != nil {
		if s, ok := proxy.RebindStatus(projectID); ok {
			rebind = &RebindReport{
				State:     s.State,
				Attempts:  s.Attempts,
				LastError: s.LastError,
			}
		}
	}

	st := sup.Status(supervisor.Key{ProjectID: projectID, Role: supervisor.RoleProxy})
	cfgPath, _ := IronProxyConfigPath(cfg, projectID)
	_, cfgErr := os.Stat(cfgPath)
	if !st.Present || !st.Running || cfgErr != nil {
		return ProxyHealth{Status: ProxyMissing, NeedsSecrets: needsSecrets, Rebind: rebind}
	}
	if snap != nil && snap.ProxyVersion != "" && snap.ProxyVersion != ironproxy.EmbeddedSha256() {
		return ProxyHealth{Status: ProxyStale, NeedsSecrets: needsSecrets, Rebind: rebind}
	}
	return ProxyHealth{Status: ProxyOK, NeedsSecrets: false, Rebind: rebind}
}

// cfgHasSecretRefs reports whether any env value (global or per-service) is
// a `!secret` reference — i.e. the project's iron-proxy injects
// host-scoped secret values.
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
