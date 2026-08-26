package serviceapi

import (
	"context"
	"io"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/ironproxy"
	"github.com/mdubb86/devm/internal/setsidshim"
	"github.com/mdubb86/devm/internal/supervisor"
)

// TestSpawnIronProxy_WrapsWithSetsidShim pins that iron-proxy is
// started via the setsid shim (not directly). Without the shim,
// iron-proxy runs in the daemon's session and dies on `launchctl
// bootout` during devm upgrade.
func TestSpawnIronProxy_WrapsWithSetsidShim(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Substitute the low-level spawn seam so this test never execs the
	// shim or iron-proxy — it only inspects the *exec.Cmd that
	// SpawnIronProxy built.
	var gotCmd *exec.Cmd
	origSpawn := ironProxySpawn
	t.Cleanup(func() { ironProxySpawn = origSpawn })
	ironProxySpawn = func(_ context.Context, _ *supervisor.Supervisor, _ supervisor.Key, cmd *exec.Cmd, _ ...io.Writer) error {
		gotCmd = cmd
		return nil
	}

	sup := supervisor.New(t.TempDir())
	proxyCfg := IronProxyConfig{
		HTTPListen:  "127.0.0.1:0",
		HTTPSListen: "127.0.0.1:0",
		CACertPath:  "/tmp/ca.crt",
		CAKeyPath:   "/tmp/ca.key",
	}
	err := SpawnIronProxy(context.Background(), identity.Prod, sup, "p-shim-test", proxyCfg, nil)
	require.NoError(t, err)
	require.NotNil(t, gotCmd)

	runDir, err := EnsureRuntimeDir(identity.Prod)
	require.NoError(t, err)
	shimPath, err := setsidshim.Ensure(runDir)
	require.NoError(t, err)
	binaryPath, err := ironproxy.Ensure(runDir)
	require.NoError(t, err)

	assert.Equal(t, shimPath, gotCmd.Path, "argv[0] (Path) must be the setsid shim")
	require.GreaterOrEqual(t, len(gotCmd.Args), 4, "want shim, binary, -config, path")
	assert.Equal(t, shimPath, gotCmd.Args[0])
	assert.Equal(t, binaryPath, gotCmd.Args[1], "argv[1] must be the iron-proxy binary path")
	assert.Equal(t, "-config", gotCmd.Args[2])
}

func TestBuildIronProxyConfig_HasExpectedFields(t *testing.T) {
	cfg := IronProxyConfig{
		HTTPListen:  "192.168.64.1:8080",
		HTTPSListen: "192.168.64.1:8443",
		DNSListen:   "192.168.64.1:8053",
		DNSProxyIP:  "192.168.64.1",
		CACertPath:  "/Users/x/Library/Application Support/devm/ca/root.crt",
		CAKeyPath:   "/Users/x/Library/Application Support/devm/ca/root.key",
		AllowList:   []string{"github.com", "*.npmjs.org"},
	}
	blob, err := cfg.YAML()
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, yaml.Unmarshal(blob, &got))

	// dns section: always enabled (Task 9b VM injection depends on it)
	dns := got["dns"].(map[string]any)
	assert.Equal(t, true, dns["enabled"])
	assert.Equal(t, "192.168.64.1:8053", dns["listen"])
	// proxy_ip is the answer iron-proxy returns for every allow-listed
	// host. Guest's DNAT rules rewrite traffic to it. Required by iron-proxy
	// 0.45+.
	assert.Equal(t, "192.168.64.1", dns["proxy_ip"])

	// proxy section
	proxy := got["proxy"].(map[string]any)
	assert.Equal(t, "192.168.64.1:8080", proxy["http_listen"])
	assert.Equal(t, "192.168.64.1:8443", proxy["https_listen"])
	assert.Equal(t, []any{}, proxy["upstream_deny_cidrs"])
	// tunnel_listen is omitted unless set — protecting adopts of
	// pre-tunnel_listen configs from a spurious field in the compare.
	_, hasTunnel := proxy["tunnel_listen"]
	assert.False(t, hasTunnel, "tunnel_listen must be absent when TunnelListen is empty")

	// tls section
	tls := got["tls"].(map[string]any)
	assert.Contains(t, tls["ca_cert"].(string), "root.crt")
	assert.Contains(t, tls["ca_key"].(string), "root.key")

	// transforms: allowlist domains live under transforms[0].config.domains
	transforms := got["transforms"].([]any)
	require.Len(t, transforms, 1)
	transform := transforms[0].(map[string]any)
	assert.Equal(t, "allowlist", transform["name"])
	transformCfg := transform["config"].(map[string]any)
	domains := transformCfg["domains"].([]any)
	assert.Equal(t, []any{"github.com", "*.npmjs.org"}, domains)
}

// Path-bearing allowlist entries emit as allowlist `rules` (host +
// paths), not as domains — iron-proxy's domains list is host-only, so
// a "host/path*" string placed there would be dead (never match).
// Bare hosts stay in domains; patterns for the same host coalesce
// into one rule; rules for distinct hosts keep first-seen order.
func TestIronProxyConfigYAML_PathEntriesEmitAsRules(t *testing.T) {
	c := IronProxyConfig{
		HTTPListen:  "127.0.0.1:8080",
		HTTPSListen: "127.0.0.1:8443",
		DNSListen:   "127.0.0.1:8053",
		DNSProxyIP:  "192.0.2.1",
		CACertPath:  "/tmp/root.crt",
		CAKeyPath:   "/tmp/root.key",
		AllowList: []string{
			"github.com",
			"release-assets.githubusercontent.com/gh-prod/834082440/*",
			"api.github.com/repos/o/r/releases",
			"release-assets.githubusercontent.com/gh-prod/916455101/*",
		},
	}
	out, err := c.YAML()
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, yaml.Unmarshal(out, &got))

	transforms := got["transforms"].([]any)
	require.Len(t, transforms, 1)
	transform := transforms[0].(map[string]any)
	assert.Equal(t, "allowlist", transform["name"])
	cfg := transform["config"].(map[string]any)

	assert.Equal(t, []any{"github.com"}, cfg["domains"].([]any),
		"path-bearing entries must not leak into domains")

	rules := cfg["rules"].([]any)
	require.Len(t, rules, 2)
	r0 := rules[0].(map[string]any)
	assert.Equal(t, "release-assets.githubusercontent.com", r0["host"])
	assert.Equal(t, []any{"/gh-prod/834082440/*", "/gh-prod/916455101/*"}, r0["paths"].([]any),
		"same-host patterns coalesce into one rule")
	r1 := rules[1].(map[string]any)
	assert.Equal(t, "api.github.com", r1["host"])
	assert.Equal(t, []any{"/repos/o/r/releases"}, r1["paths"].([]any))
}

// A path-free AllowList must not emit a rules key at all — the
// domains-only shape is what adopt-in-place compares against for
// pre-existing configs.
func TestIronProxyConfigYAML_NoRulesKeyWithoutPathEntries(t *testing.T) {
	c := IronProxyConfig{
		HTTPListen:  "127.0.0.1:8080",
		HTTPSListen: "127.0.0.1:8443",
		DNSListen:   "127.0.0.1:8053",
		DNSProxyIP:  "192.0.2.1",
		CACertPath:  "/tmp/root.crt",
		CAKeyPath:   "/tmp/root.key",
		AllowList:   []string{"github.com"},
	}
	out, err := c.YAML()
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, yaml.Unmarshal(out, &got))
	cfg := got["transforms"].([]any)[0].(map[string]any)["config"].(map[string]any)
	_, hasRules := cfg["rules"]
	assert.False(t, hasRules, "rules key must be absent when no entry carries a path")
}

// An empty AllowList must still emit the allowlist transform, with no
// domains. iron-proxy v0.45.0 runs no egress check at all when the
// transform is absent — an omitted transform is allow-all. The
// deny-all shape is the transform present with `domains: []`.
// tunnel_listen is iron-proxy's CONNECT/SOCKS5 tunnel port. HTTP_PROXY-
// consuming clients (git during host-side hydration) MUST reach the tunnel
// port — the http_listen handler returns 400 for CONNECT. If this test
// fails, hydration's git subprocess will 400 on every CONNECT attempt.
// Pinned separately from the earlier YAML shape test to signal intent:
// this field's presence is load-bearing for hydration, not incidental.
func TestBuildIronProxyConfig_EmitsTunnelListenWhenSet(t *testing.T) {
	cfg := IronProxyConfig{
		HTTPListen:   "127.0.0.1:8080",
		HTTPSListen:  "127.0.0.1:8443",
		TunnelListen: "127.0.0.1:8081",
		DNSListen:    "127.0.0.1:8053",
		DNSProxyIP:   "127.0.0.1",
		CACertPath:   "/tmp/ca.crt",
		CAKeyPath:    "/tmp/ca.key",
	}
	blob, err := cfg.YAML()
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, yaml.Unmarshal(blob, &got))
	proxy := got["proxy"].(map[string]any)
	assert.Equal(t, "127.0.0.1:8081", proxy["tunnel_listen"],
		"tunnel_listen must be present in the emitted YAML so iron-proxy starts its CONNECT accept loop")
}

// ironProxyURLFor is the URL git hydration hands to HTTP_PROXY / HTTPS_PROXY.
// It MUST point at the tunnel port — the http_listen handler returns 400 for
// CONNECT. A regression to http_listen here means every git-clone hydration
// attempts a CONNECT the http handler rejects.
func TestIronProxyURLFor_ReturnsTunnelPortNotHTTPPort(t *testing.T) {
	ironProxyState = newIronProxyStore()
	t.Cleanup(func() { ironProxyState = newIronProxyStore() })

	ironProxyState.put("p", projectInfo{HTTPPort: 8080, TunnelPort: 8081})
	got := ironProxyURLFor("p")
	assert.Contains(t, got, ":8081",
		"ironProxyURLFor must return the tunnel port URL; got %q", got)
	assert.NotContains(t, got, ":8080",
		"ironProxyURLFor must NOT return the http_listen port — CONNECT there returns 400; got %q", got)
}

// ironProxyURLFor returns empty for a project the state store doesn't know
// about (called before SpawnIronProxy). Hydration checks the returned URL
// and skips proxy setup when empty, so the caller must never see ":0".
func TestIronProxyURLFor_EmptyForUnknownProject(t *testing.T) {
	ironProxyState = newIronProxyStore()
	t.Cleanup(func() { ironProxyState = newIronProxyStore() })

	assert.Equal(t, "", ironProxyURLFor("never-spawned"))
}

func TestBuildIronProxyConfig_EmptyAllowList_EmitsDenyAllTransform(t *testing.T) {
	cfg := IronProxyConfig{
		HTTPListen:  "127.0.0.1:8080",
		HTTPSListen: "127.0.0.1:8443",
		CACertPath:  "/tmp/ca.crt",
		CAKeyPath:   "/tmp/ca.key",
	}
	blob, err := cfg.YAML()
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, yaml.Unmarshal(blob, &got))

	transforms, ok := got["transforms"].([]any)
	require.True(t, ok, "transforms key must be present even with an empty AllowList")
	require.Len(t, transforms, 1)
	transform := transforms[0].(map[string]any)
	assert.Equal(t, "allowlist", transform["name"])
	assert.Equal(t, []any{}, transform["config"].(map[string]any)["domains"])
}

func TestIronProxyListenAddr_UsesLoopback(t *testing.T) {
	// softnet dials iron-proxy host-side — there's no vmnet bridge under
	// --net-softnet, so iron-proxy always binds loopback.
	assert.Equal(t, "127.0.0.1:8080", ironProxyListenAddr(8080))
	assert.Equal(t, "127.0.0.1:8443", ironProxyListenAddr(8443))
}

func TestSecretEnvVarName(t *testing.T) {
	assert.Equal(t, "DEVM_SECRET_GITHUB_TOKEN", secretEnvVarName("github_token"))
	assert.Equal(t, "DEVM_SECRET_ANTHROPIC_API_KEY", secretEnvVarName("anthropic_api_key"))
}

// helper: pull the `secrets` transform's secret entries out of emitted YAML.
func secretEntries(t *testing.T, blob []byte) []map[string]any {
	t.Helper()
	var got map[string]any
	require.NoError(t, yaml.Unmarshal(blob, &got))
	transforms, _ := got["transforms"].([]any)
	for _, tr := range transforms {
		tm := tr.(map[string]any)
		if tm["name"] == "secrets" {
			conf := tm["config"].(map[string]any)
			raw := conf["secrets"].([]any)
			out := make([]map[string]any, 0, len(raw))
			for _, s := range raw {
				out = append(out, s.(map[string]any))
			}
			return out
		}
	}
	return nil
}

func TestIronProxy_SecretEmission_ReplaceNestingAndRules(t *testing.T) {
	cfg := IronProxyConfig{
		HTTPListen: "x:1", HTTPSListen: "x:2", CACertPath: "/c", CAKeyPath: "/k",
		AllowList: []string{"*"},
		Secrets: []IronSecret{
			{Name: "github_token", Value: "real-gh", Hosts: []string{"api.github.com", "uploads.github.com"}},
		},
	}
	blob, err := cfg.YAML()
	require.NoError(t, err)

	entries := secretEntries(t, blob)
	require.Len(t, entries, 1)
	e := entries[0]

	// source
	src := e["source"].(map[string]any)
	assert.Equal(t, "env", src["type"])
	assert.Equal(t, "DEVM_SECRET_GITHUB_TOKEN", src["var"])

	// replace block (NOT top-level)
	rep := e["replace"].(map[string]any)
	assert.Equal(t, "__DEVM_SECRET_github_token__", rep["proxy_value"])
	assert.Equal(t, []any{}, rep["match_headers"]) // [] = all headers
	assert.Equal(t, true, rep["match_query"], "query params must be substituted too")
	assert.Nil(t, rep["match_path"], "path substitution does not escape / — must stay off")
	assert.Nil(t, rep["match_body"], "body substitution forces request buffering — must stay off")
	assert.Nil(t, e["proxy_value"], "proxy_value must be under replace:, not top-level")

	// rules: one {host} per bound host, sibling of source/replace
	rules := e["rules"].([]any)
	require.Len(t, rules, 2)
	assert.Equal(t, "api.github.com", rules[0].(map[string]any)["host"])
	assert.Equal(t, "uploads.github.com", rules[1].(map[string]any)["host"])

	// real value never in YAML
	assert.NotContains(t, string(blob), "real-gh")
}

func TestIronProxy_SecretWithNoHosts_Omitted(t *testing.T) {
	cfg := IronProxyConfig{
		HTTPListen: "x:1", HTTPSListen: "x:2", CACertPath: "/c", CAKeyPath: "/k",
		AllowList: []string{"*"},
		Secrets:   []IronSecret{{Name: "unbound", Value: "real", Hosts: nil}},
	}
	blob, err := cfg.YAML()
	require.NoError(t, err)
	assert.NotContains(t, string(blob), "name: secrets", "unbound secret must not produce a secrets transform")
	assert.NotContains(t, string(blob), "real")
}

func TestBuildIronProxyConfig_EnablesDNSWhenListenSet(t *testing.T) {
	cfg := IronProxyConfig{
		HTTPListen:  "192.168.64.1:8080",
		HTTPSListen: "192.168.64.1:8443",
		DNSListen:   "192.168.64.1:8053",
		CACertPath:  "/c",
		CAKeyPath:   "/k",
	}
	blob, err := cfg.YAML()
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, yaml.Unmarshal(blob, &got))

	dns := got["dns"].(map[string]any)
	assert.Equal(t, true, dns["enabled"])
	assert.Equal(t, "192.168.64.1:8053", dns["listen"])
}

func TestIronProxy_EnvVars_OnlyBoundSecrets(t *testing.T) {
	cfg := IronProxyConfig{
		Secrets: []IronSecret{
			{Name: "foo", Value: "value-1", Hosts: []string{"api.foo.com"}},
			{Name: "unbound", Value: "value-2", Hosts: nil},
		},
	}
	got := cfg.EnvVars()
	assert.Equal(t, []string{"DEVM_SECRET_FOO=value-1"}, got)
}
