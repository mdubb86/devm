package serviceapi

import (
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
