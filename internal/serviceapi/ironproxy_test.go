package serviceapi

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestBuildIronProxyConfig_HasExpectedFields(t *testing.T) {
	cfg := IronProxyConfig{
		HTTPListen:  "192.168.64.1:8080",
		HTTPSListen: "192.168.64.1:8443",
		CACertPath:  "/Users/x/Library/Application Support/devm/ca/root.crt",
		CAKeyPath:   "/Users/x/Library/Application Support/devm/ca/root.key",
		AllowList:   []string{"github.com", "*.npmjs.org"},
	}
	blob, err := cfg.YAML()
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, yaml.Unmarshal(blob, &got))

	// dns section: disabled in all daemon-spawned instances
	dns := got["dns"].(map[string]any)
	assert.Equal(t, false, dns["enabled"])

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

func TestSecretEnvVarName(t *testing.T) {
	assert.Equal(t, "DEVM_SECRET_GITHUB_TOKEN", secretEnvVarName("__DEVM_SECRET_github_token__"))
	assert.Equal(t, "DEVM_SECRET_ANTHROPIC_API_KEY", secretEnvVarName("__DEVM_SECRET_anthropic_api_key__"))
}

func TestBuildIronProxyConfig_EmitsSecretsTransformWhenTokensPresent(t *testing.T) {
	cfg := IronProxyConfig{
		HTTPListen:   "x:1",
		HTTPSListen:  "x:2",
		CACertPath:   "/c",
		CAKeyPath:    "/k",
		SecretTokens: map[string]string{"__DEVM_SECRET_foo__": "real"},
	}
	blob, err := cfg.YAML()
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, yaml.Unmarshal(blob, &got))

	transforms := got["transforms"].([]any)
	// Find the secrets transform.
	var secretsT map[string]any
	for _, tr := range transforms {
		tm := tr.(map[string]any)
		if tm["name"] == "secrets" {
			secretsT = tm
			break
		}
	}
	require.NotNil(t, secretsT, "secrets transform missing")

	conf := secretsT["config"].(map[string]any)
	secrets := conf["secrets"].([]any)
	require.Len(t, secrets, 1)
	first := secrets[0].(map[string]any)
	assert.Equal(t, "__DEVM_SECRET_foo__", first["proxy_value"])
	src := first["source"].(map[string]any)
	assert.Equal(t, "env", src["type"])
	assert.Equal(t, "DEVM_SECRET_FOO", src["var"])

	// Real secret value NOT in YAML.
	assert.NotContains(t, string(blob), "real")
}

func TestBuildIronProxyConfig_NoSecretsTransformWhenEmpty(t *testing.T) {
	cfg := IronProxyConfig{HTTPListen: "x:1", HTTPSListen: "x:2", CACertPath: "/c", CAKeyPath: "/k"}
	blob, err := cfg.YAML()
	require.NoError(t, err)

	// Either no transforms key, or transforms present but no `secrets` entry.
	if !strings.Contains(string(blob), "transforms") {
		return
	}
	assert.NotContains(t, string(blob), "name: secrets")
}

func TestIronProxyConfig_EnvVars(t *testing.T) {
	cfg := IronProxyConfig{
		SecretTokens: map[string]string{
			"__DEVM_SECRET_foo__": "value-1",
			"__DEVM_SECRET_bar__": "value-2",
		},
	}
	got := cfg.EnvVars()
	assert.ElementsMatch(t, []string{"DEVM_SECRET_FOO=value-1", "DEVM_SECRET_BAR=value-2"}, got)
}
