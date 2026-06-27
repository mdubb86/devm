package serviceapi

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/mdubb86/devm/internal/ironproxy"
	"github.com/mdubb86/devm/internal/supervisor"
	"gopkg.in/yaml.v3"
)

// IronProxyConfig is iron-proxy v0.45.0's YAML config shape (verified
// against e2e/test_iron_contract_01_*.py and e2e/helpers/iron_proxy.py).
// Fields use YAML names because we marshal to YAML on disk.
type IronProxyConfig struct {
	HTTPListen  string   // proxy.http_listen
	HTTPSListen string   // proxy.https_listen
	CACertPath  string   // tls.ca_cert
	CAKeyPath   string   // tls.ca_key
	AllowList   []string // transforms[{name:"allowlist"}].config.domains
	// Note: secret substitution tokens are injected into the VM's
	// environment directly (Task 9); iron-proxy v0.45.0 has no
	// native secrets field in its config schema.
}

// YAML returns the YAML blob iron-proxy reads from -config <path>.
// The schema matches e2e/helpers/iron_proxy.py's IronProxyConfig.to_yaml_dict().
func (c IronProxyConfig) YAML() ([]byte, error) {
	raw := map[string]any{
		"dns": map[string]any{
			"enabled": false,
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
	if len(c.AllowList) > 0 {
		raw["transforms"] = []any{
			map[string]any{
				"name": "allowlist",
				"config": map[string]any{
					"domains": c.AllowList,
				},
			},
		}
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
	key := supervisor.Key{ProjectID: projectID, Role: supervisor.RoleProxy}
	return sup.Spawn(ctx, key, cmd)
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
