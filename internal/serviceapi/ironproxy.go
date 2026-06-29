package serviceapi

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mdubb86/devm/internal/ironproxy"
	"github.com/mdubb86/devm/internal/supervisor"
	"gopkg.in/yaml.v3"
)

// IronSecret is one host-scoped secret to substitute. Value is the real
// secret (goes into iron-proxy's process env, never the on-disk YAML).
// Hosts are the upstreams the secret may be injected for; empty Hosts
// means the secret is omitted entirely (never injected).
type IronSecret struct {
	Name  string
	Value string
	Hosts []string
}

// IronProxyConfig is iron-proxy v0.45.0's YAML config shape.
type IronProxyConfig struct {
	HTTPListen  string
	HTTPSListen string
	DNSListen   string
	CACertPath  string
	CAKeyPath   string
	AllowList   []string
	Secrets     []IronSecret
}

// YAML returns the YAML blob iron-proxy reads from -config <path>.
// The schema matches e2e/helpers/iron_proxy.py's IronProxyConfig.to_yaml_dict().
func (c IronProxyConfig) YAML() ([]byte, error) {
	raw := map[string]any{
		"dns": map[string]any{
			"enabled": true,
			"listen":  c.DNSListen,
		},
		"proxy": map[string]any{
			"http_listen":         c.HTTPListen,
			"https_listen":        c.HTTPSListen,
			// Allow loopback upstream so in-VM services can be reached.
			// Overrides iron-proxy's default deny for 127.0.0.0/8.
			"upstream_deny_cidrs": []string{},
		},
		"tls": map[string]any{
			"ca_cert": c.CACertPath,
			"ca_key":  c.CAKeyPath,
		},
	}
	var transforms []any
	if len(c.AllowList) > 0 {
		transforms = append(transforms, map[string]any{
			"name": "allowlist",
			"config": map[string]any{
				"domains": c.AllowList,
			},
		})
	}
	var boundSecrets []IronSecret
	for _, s := range c.Secrets {
		if len(s.Hosts) > 0 {
			boundSecrets = append(boundSecrets, s)
		}
	}
	if len(boundSecrets) > 0 {
		var entries []any
		for _, s := range boundSecrets {
			rules := make([]any, 0, len(s.Hosts))
			for _, h := range s.Hosts {
				rules = append(rules, map[string]any{"host": h})
			}
			entries = append(entries, map[string]any{
				"source": map[string]any{
					"type": "env",
					"var":  secretEnvVarName(s.Name),
				},
				// match_* fields MUST nest under `replace:`; at top level
				// iron-proxy silently ignores match_query/body/path.
				"replace": map[string]any{
					"proxy_value":   secretToken(s.Name),
					"match_headers": []string{}, // [] = scan all request headers (incl. cookies)
				},
				"rules": rules,
			})
		}
		transforms = append(transforms, map[string]any{
			"name": "secrets",
			"config": map[string]any{
				"secrets": entries,
			},
		})
	}
	if len(transforms) > 0 {
		raw["transforms"] = transforms
	}
	return yaml.Marshal(raw)
}

// SpawnIronProxy starts iron-proxy via the supervisor with a freshly
// written config file at a stable per-project path. The file is mode
// 0600, user-owned. Idempotent at the supervisor level — if a process
// with the same key is already running it is replaced by the new one.
//
// Note: iron-proxy v0.45.0 doesn't accept config on stdin, so the
// config lands on disk. Mitigated by file mode 0600 under the user's
// runtime dir (~/Library/Application Support/devm/). Future improvement:
// contribute stdin support upstream and switch.
func SpawnIronProxy(ctx context.Context, sup *supervisor.Supervisor, projectID string, cfg IronProxyConfig) error {
	binary, err := ironproxy.Path()
	if err != nil {
		return fmt.Errorf("locate iron-proxy: %w", err)
	}
	blob, err := cfg.YAML()
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	configPath, err := writeIronProxyConfig(projectID, blob)
	if err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	cmd := exec.CommandContext(ctx, binary, "-config", configPath)
	cmd.Env = append(os.Environ(), cfg.EnvVars()...)
	key := supervisor.Key{ProjectID: projectID, Role: supervisor.RoleProxy}
	return sup.Spawn(ctx, key, cmd)
}

// EnvVars returns KEY=VALUE strings for iron-proxy's process env, one per
// host-bound secret. Unbound secrets are skipped — their value never
// reaches the proxy. Values never touch the on-disk config.
func (c IronProxyConfig) EnvVars() []string {
	out := make([]string, 0, len(c.Secrets))
	for _, s := range c.Secrets {
		if len(s.Hosts) == 0 {
			continue
		}
		out = append(out, fmt.Sprintf("%s=%s", secretEnvVarName(s.Name), s.Value))
	}
	return out
}

// secretToken is the opaque placeholder the VM carries and iron-proxy
// swaps for the real value. Must match schema.TokenFor.
func secretToken(name string) string {
	return "__DEVM_SECRET_" + name + "__"
}

// secretEnvVarName is the process-env var iron-proxy reads the real value
// from. "github_token" → "DEVM_SECRET_GITHUB_TOKEN".
func secretEnvVarName(name string) string {
	return "DEVM_SECRET_" + strings.ToUpper(name)
}

// writeIronProxyConfig persists the YAML blob to a stable per-project path
// so the supervisor can re-spawn iron-proxy after a crash without re-running
// the daemon's config-build path. Returns the absolute path. File is written
// mode 0600 to limit exposure of the config contents.
func writeIronProxyConfig(projectID string, blob []byte) (string, error) {
	runDir, err := EnsureRuntimeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(runDir, "iron-proxy")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create iron-proxy config dir: %w", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("%s.yaml", projectID))
	if err := os.WriteFile(path, blob, 0600); err != nil {
		return "", fmt.Errorf("write iron-proxy config: %w", err)
	}
	return path, nil
}
