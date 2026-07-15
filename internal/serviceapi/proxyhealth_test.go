package serviceapi

import (
	"testing"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/supervisor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCfgHasSecretRefs(t *testing.T) {
	none := schema.Config{Env: map[string]schema.EnvValue{"A": {Literal: "x"}}}
	assert.False(t, cfgHasSecretRefs(none))
	withSecret := schema.Config{Env: map[string]schema.EnvValue{"A": {Secret: &schema.SecretRef{Name: "TOK"}}}}
	assert.True(t, cfgHasSecretRefs(withSecret))
}

func TestComputeProxyHealth(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", t.TempDir())
	sup := supervisor.New("")
	// No proxy, no config file → MISSING.
	h := computeProxyHealth(sup, "p")
	assert.Equal(t, ProxyMissing, h.Status)
	assert.False(t, h.NeedsSecrets) // no snapshot → no secret refs known
	// Write a snapshot with a secret ref + a config file + stamp mismatch → still MISSING (no live proxy) + NeedsSecrets true.
	require.NoError(t, WriteStateSnapshot("p", StateSnapshot{
		Cfg:          schema.Config{Env: map[string]schema.EnvValue{"A": {Secret: &schema.SecretRef{Name: "T"}}}},
		ProxyVersion: "old",
	}))
	// (config-file presence + live-proxy cases are exercised in the integration test in Task 8;
	//  here assert the secret-ref half is wired.)
	h = computeProxyHealth(sup, "p")
	assert.Equal(t, ProxyMissing, h.Status)
	assert.True(t, h.NeedsSecrets)
}
