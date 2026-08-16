package serviceapi

import (
	"context"
	"io"
	"os/exec"
	"path/filepath"
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

func TestLoadIronProxyConfig_RoundTrip(t *testing.T) {
	in := IronProxyConfig{
		HTTPListen:  "127.42.0.2:40080",
		HTTPSListen: "127.42.0.2:40443",
		DNSListen:   "127.42.0.2:40053",
		DNSProxyIP:  "192.168.127.1",
		CACertPath:  "/tmp/ca/root.crt",
		CAKeyPath:   "/tmp/ca/root.key",
		AllowList:   []string{"deb.debian.org", "api.anthropic.com"},
		Secrets:     []IronSecret{{Name: "tok", Value: "s3cret", Hosts: []string{"api.example.com"}}},
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, writeIronProxyConfig(path, in))
	got, err := LoadIronProxyConfig(path)
	require.NoError(t, err)
	require.Equal(t, in, got)
}

func TestBuildIronProxyConfig_EmptyAllowList_OmitsTransforms(t *testing.T) {
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

	_, hasTransforms := got["transforms"]
	assert.False(t, hasTransforms, "transforms key should be absent when AllowList is empty")
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
