package serviceapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/ironproxy"
	"github.com/mdubb86/devm/internal/setsidshim"
	"github.com/mdubb86/devm/internal/softnet"
	"github.com/mdubb86/devm/internal/supervisor"
	"gopkg.in/yaml.v3"
)

// ironProxySpawn is the test-injection seam for the actual process
// spawn inside SpawnIronProxy. Production always delegates to
// sup.Spawn; tests substitute a fake to capture the constructed
// *exec.Cmd (argv, path) without actually exec'ing the shim or
// iron-proxy.
var ironProxySpawn = func(ctx context.Context, sup *supervisor.Supervisor, key supervisor.Key, cmd *exec.Cmd, taps ...io.Writer) error {
	return sup.Spawn(ctx, key, cmd, taps...)
}

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
	// TunnelListen is iron-proxy's CONNECT/SOCKS5 tunnel port. It runs
	// on its own accept loop (iron-proxy internal/proxy/tunnel.go) and
	// is the only listener that handles the `CONNECT host:port` method
	// — the http_listen handler returns 400 for CONNECT. Any Mac-side
	// client that uses HTTP_PROXY / HTTPS_PROXY (`git clone` during
	// host-side repo hydration is the load-bearing case) MUST dial this
	// port, not http_listen. Empty ⇒ iron-proxy never starts the tunnel
	// handler at all, which strands HTTP_PROXY-consuming callers with
	// CONNECT-returns-400.
	TunnelListen string
	DNSListen    string
	// DNSProxyIP is the IP iron-proxy answers with for every host in the
	// allow list. softnet forwards outbound TCP:80/443 to iron-proxy's
	// HTTP/HTTPS listeners by destination port under ENFORCED policy, so
	// traffic addressed to that IP reaches iron-proxy the same as any
	// other allow-listed destination. Required by iron-proxy 0.45+; empty
	// causes iron-proxy to exit with "dns.proxy_ip is required".
	DNSProxyIP string
	CACertPath string
	CAKeyPath  string
	// AllowList is the project's effective network.allow set. It is NOT
	// rendered into iron-proxy's YAML — the allow/deny decision lives in
	// the daemon's PolicyAuthority, which iron-proxy consults per
	// request via the grpc transform at PolicyTarget. SpawnIronProxy
	// feeds this list into the authority; after a daemon restart it is
	// recomputed from the state snapshot rather than persisted anywhere
	// (see recoverProjectState in ironproxy_discover.go).
	AllowList []string
	// PolicyTarget is the grpc transform's target — the unix socket the
	// daemon serves TransformService on for this project
	// ("unix:///path/to.sock"). Required: YAML() refuses to render
	// without it, because iron-proxy treats an absent policy transform
	// as allow-all.
	PolicyTarget string
	Secrets      []IronSecret
}

// ironProxyListenAddr is the address iron-proxy binds one of its HTTP,
// HTTPS, or DNS listeners on. softnet dials iron-proxy host-side — under
// --net-softnet there's no vmnet bridge for the guest to reach, so the
// bind host is always loopback.
func ironProxyListenAddr(port int) string {
	return fmt.Sprintf("%s:%d", softnet.HostLoopIP, port)
}

// ironProxyURLFor returns the http:// URL that HTTP_PROXY/HTTPS_PROXY-aware
// tooling (git, during host-side repo hydration) dials to route through
// this project's iron-proxy instance. Returns iron-proxy's tunnel_listen
// port, not http_listen: HTTP_PROXY clients send `CONNECT host:port`
// which the http_listen handler rejects with 400 — tunnel_listen owns
// the CONNECT accept loop. Empty when iron-proxy state hasn't been
// seeded for projectID yet (e.g. called before SpawnIronProxy).
func ironProxyURLFor(projectID string) string {
	info, ok := ironProxyState.get(projectID)
	if !ok || info.TunnelPort == 0 {
		return ""
	}
	return "http://" + ironProxyListenAddr(info.TunnelPort)
}

// YAML returns the YAML blob iron-proxy reads from -config <path>.
// The schema matches e2e/helpers/iron_proxy.py's IronProxyConfig.to_yaml_dict().
func (c IronProxyConfig) YAML() ([]byte, error) {
	raw := map[string]any{
		"dns": map[string]any{
			"enabled":  true,
			"listen":   c.DNSListen,
			"proxy_ip": c.DNSProxyIP,
		},
		"proxy": func() map[string]any {
			m := map[string]any{
				"http_listen":  c.HTTPListen,
				"https_listen": c.HTTPSListen,
				// Allow loopback upstream so in-VM services can be reached.
				// Overrides iron-proxy's default deny for 127.0.0.0/8.
				"upstream_deny_cidrs": []string{},
			}
			if c.TunnelListen != "" {
				m["tunnel_listen"] = c.TunnelListen
			}
			return m
		}(),
		"tls": map[string]any{
			"ca_cert": c.CACertPath,
			"ca_key":  c.CAKeyPath,
		},
		// Metrics listen on a loopback ephemeral port. Loopback-only
		// because iron-proxy v0.45.0's metrics server exposes only
		// /healthz (no /metrics, no /debug/pprof) — nothing worth
		// reaching from LAN, and no reason to publish even /healthz
		// there. Ephemeral port because per-project iron-proxy
		// instances would otherwise fight over the built-in default
		// of :9090.
		"metrics": map[string]any{
			"listen": "127.0.0.1:0",
		},
	}
	// The policy transform is emitted unconditionally. iron-proxy runs
	// the egress check only when a transform is present: with it absent
	// every host is proxied and allowed, so omitting it would publish an
	// unrestricted egress path. The transform is `grpc`, delegating each
	// request's allow/deny to the daemon's PolicyAuthority at
	// PolicyTarget; if the daemon is unreachable iron-proxy fails closed
	// with a 502 (pinned by test_iron_contract_09).
	if c.PolicyTarget == "" {
		return nil, fmt.Errorf("iron-proxy config: PolicyTarget is required — an absent policy transform would be allow-all")
	}
	transforms := []any{map[string]any{
		"name": "grpc",
		"config": map[string]any{
			"name":   "devm-policy",
			"target": c.PolicyTarget,
		},
	}}
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
				//
				// match_query is a bool (a list fails to unmarshal). It
				// covers APIs that take the credential as a query param.
				// Safe because iron-proxy re-encodes query values after
				// substitution, so a secret containing &, =, + or / can't
				// break out of its parameter.
				//
				// match_path is deliberately NOT set: path substitution
				// writes through url.URL.Path, which does not escape "/",
				// so a secret containing a slash would silently become an
				// extra path segment. match_body is off too — it forces
				// the proxy to buffer request bodies.
				"replace": map[string]any{
					"proxy_value":   secretToken(s.Name),
					"match_headers": []string{}, // [] = scan all request headers (incl. cookies)
					"match_query":   true,
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
	raw["transforms"] = transforms
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
func SpawnIronProxy(ctx context.Context, cfg identity.Config, sup *supervisor.Supervisor, projectID string, proxyCfg IronProxyConfig) error {
	runDir, err := EnsureRuntimeDir(cfg)
	if err != nil {
		return fmt.Errorf("runtime dir: %w", err)
	}
	binary, err := ironproxy.Ensure(runDir)
	if err != nil {
		return fmt.Errorf("locate iron-proxy: %w", err)
	}
	shim, err := setsidshim.Ensure(runDir)
	if err != nil {
		return fmt.Errorf("locate setsid shim: %w", err)
	}
	configPath, err := IronProxyConfigPath(cfg, projectID)
	if err != nil {
		return fmt.Errorf("config path: %w", err)
	}

	// Stand up this project's policy authority BEFORE iron-proxy spawns:
	// the daemon owns allow/deny (served over the unix socket the grpc
	// transform dials), and a proxy that comes up first would fail
	// closed with 502s until the socket exists. After a daemon restart,
	// AdoptIronProxies re-serves the socket with the allowlist
	// recomputed from the state snapshot.
	sockPath, err := IronPolicySocketPath(cfg, projectID)
	if err != nil {
		return fmt.Errorf("policy socket path: %w", err)
	}
	policyAuthority.SetAllowlist(projectID, proxyCfg.AllowList)
	if err := policyAuthority.EnsureServing(projectID, sockPath); err != nil {
		return fmt.Errorf("serve policy: %w", err)
	}
	proxyCfg.PolicyTarget = "unix://" + sockPath

	if err := writeIronProxyConfig(configPath, proxyCfg); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	// iron-proxy is started via the devm-setsid-shim (argv[0]) rather
	// than directly, so it lands in its own session — detached from
	// the daemon's process tree — and survives launchctl bootout of
	// the daemon during devm install/upgrade. See internal/setsidshim.
	cmd := exec.CommandContext(ctx, shim, binary, "-config", configPath)
	cmd.Env = append(os.Environ(), proxyCfg.EnvVars()...)
	key := supervisor.Key{ProjectID: projectID, Role: supervisor.RoleProxy}
	if err := ironProxySpawn(ctx, sup, key, cmd); err != nil {
		return err
	}

	// Discover the grandchild iron-proxy PID (the actual process behind
	// the shim) and teach the supervisor about it. This is what lets
	// supervisor.Stop signal iron-proxy directly instead of the shim,
	// which is critical: the shim ignores SIGTERM by design so
	// launchd's bootout of the daemon can't reach through it. Without
	// this handoff, supervisor.Stop would signal the shim (no-op),
	// wait pexec's StopTimeout, then SIGKILL the shim — leaving
	// iron-proxy orphaned to init rather than gracefully stopped.
	//
	// Poll DiscoverIronProxies because iron-proxy takes ~500ms to
	// appear in ps (fork+exec+setsid, then arg-parse before it's
	// long-lived enough to matter). Bounded wait; if it never appears
	// the spawn is broken and Stop will fall back to pexec's Stop
	// (slow but correct).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		procs, err := DiscoverIronProxies(ctx, cfg)
		if err == nil {
			for _, p := range procs {
				if p.ProjectID == projectID {
					sup.SetChildPID(key, p.PID)
					return nil
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Grandchild not found within grace. Non-fatal: Spawn succeeded
	// (the shim is running); Stop will fall back to pexec's path.
	// Something else has probably gone wrong that will surface soon.
	return nil
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

// IronPolicySocketPath returns the unix socket the daemon serves this
// project's TransformService on — the grpc transform's dial target.
// Named by projectID for debuggability, falling back to a sha256-derived
// short name when the full path would exceed macOS's 104-byte sun_path
// cap (bind fails with EINVAL past it; 100 leaves headroom).
func IronPolicySocketPath(cfg identity.Config, projectID string) (string, error) {
	runDir, err := EnsureRuntimeDir(cfg)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(runDir, "iron-proxy")
	p := filepath.Join(dir, projectID+".sock")
	if len(p) > 100 {
		sum := sha256.Sum256([]byte(projectID))
		p = filepath.Join(dir, hex.EncodeToString(sum[:4])+".sock")
	}
	if len(p) <= 100 {
		return p, nil
	}
	// Deep runtime dirs (long $HOME) blow macOS's 104-byte sun_path cap.
	// Fall back to the per-user temp dir with a name derived from
	// identity+project so the path stays deterministic across daemon
	// restarts (adoption re-derives it) and distinct across identities.
	sum := sha256.Sum256([]byte(cfg.Name + "/" + projectID))
	p = filepath.Join(os.TempDir(), "devm-pol-"+hex.EncodeToString(sum[:6])+".sock")
	if len(p) > 100 {
		return "", fmt.Errorf("policy socket path too long even in temp dir (%d bytes): %s", len(p), p)
	}
	return p, nil
}

// IronProxyConfigPath returns the on-disk path SpawnIronProxy writes
// its config to for projectID. Used at adoption time to rehydrate
// ironProxyState from the running iron-proxy's config file. Callers
// don't need the file to exist; they get the expected location so
// they can read it or bail on ENOENT.
func IronProxyConfigPath(cfg identity.Config, projectID string) (string, error) {
	runDir, err := EnsureRuntimeDir(cfg)
	if err != nil {
		return "", err
	}
	return filepath.Join(runDir, "iron-proxy", fmt.Sprintf("%s.yaml", projectID)), nil
}

// writeIronProxyConfig persists cfg's YAML to path so the supervisor can
// re-spawn iron-proxy after a crash without re-running the daemon's
// config-build path. File is written mode 0600 to limit exposure of the
// config contents.
func writeIronProxyConfig(path string, cfg IronProxyConfig) error {
	blob, err := cfg.YAML()
	if err != nil {
		return fmt.Errorf("encode iron-proxy config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create iron-proxy config dir: %w", err)
	}
	if err := os.WriteFile(path, blob, 0600); err != nil {
		return fmt.Errorf("write iron-proxy config: %w", err)
	}
	return nil
}
