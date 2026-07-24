package serviceapi

import (
	"testing"

	"github.com/mdubb86/devm/internal/identity"
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
	t.Setenv("HOME", t.TempDir())
	sup := supervisor.New(t.TempDir())
	// No proxy, no config file → MISSING.
	h := computeProxyHealth(identity.Prod, sup, nil, "p")
	assert.Equal(t, ProxyMissing, h.Status)
	assert.False(t, h.NeedsSecrets) // no snapshot → no secret refs known
	// Write a snapshot with a secret ref + a config file + stamp mismatch → still MISSING (no live proxy) + NeedsSecrets true.
	require.NoError(t, WriteStateSnapshot(identity.Prod, "p", StateSnapshot{
		Cfg:          schema.Config{Env: map[string]schema.EnvValue{"A": {Secret: &schema.SecretRef{Name: "T"}}}},
		ProxyVersion: "old",
	}))
	// (config-file presence + live-proxy cases are exercised in the integration test in Task 8;
	//  here assert the secret-ref half is wired.)
	h = computeProxyHealth(identity.Prod, sup, nil, "p")
	assert.Equal(t, ProxyMissing, h.Status)
	assert.True(t, h.NeedsSecrets)
}

// TestComputeProxyHealth_IncludesRebindStatus proves the rebind
// outcome recorded on the ProxyServer surfaces in the ProxyHealth
// returned to status callers — needed for the CLI to warn the user
// when :80/:443 stayed unbound after a daemon restart.
func TestComputeProxyHealth_IncludesRebindStatus(t *testing.T) {
	sup := supervisor.New(t.TempDir())
	caDir := t.TempDir()
	ca, err := loadOrGenerateCAAt(identity.Prod, caDir)
	require.NoError(t, err)
	proxy := NewProxyServer(identity.Prod, NewRoutes(), ca)
	proxy.RecordRebindStatus("p", RebindStatus{
		State:     RebindFailed,
		Attempts:  3,
		LastError: "bind :80: helper: connection refused",
	})

	h := computeProxyHealth(identity.Prod, sup, proxy, "p")
	require.NotNil(t, h.Rebind, "Rebind must be populated when a rebind was recorded")
	assert.Equal(t, RebindFailed, h.Rebind.State)
	assert.Equal(t, 3, h.Rebind.Attempts)
	assert.Contains(t, h.Rebind.LastError, "connection refused")
}

// TestComputeProxyHealth_RebindNilWhenNoAttempt proves the field is
// omitted when the project never had a rebind pass (fresh install,
// or the project wasn't running at daemon startup).
func TestComputeProxyHealth_RebindNilWhenNoAttempt(t *testing.T) {
	sup := supervisor.New(t.TempDir())
	caDir := t.TempDir()
	ca, err := loadOrGenerateCAAt(identity.Prod, caDir)
	require.NoError(t, err)
	proxy := NewProxyServer(identity.Prod, NewRoutes(), ca)

	h := computeProxyHealth(identity.Prod, sup, proxy, "p")
	assert.Nil(t, h.Rebind)
}
