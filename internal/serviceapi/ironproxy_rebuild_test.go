package serviceapi

import (
	"testing"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/secret"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRebuildIronProxyConfig verifies the daemon can rebuild a full
// spawn config for a project without any CLI-supplied secret values —
// ports come from the on-disk iron-proxy config, allowlist + secret
// refs come from the passed-in schema.Config, and secret VALUES are
// resolved directly from the file-backed secret store.
func TestRebuildIronProxyConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const projectID = "rebuild-test"

	// Ports config a prior spawn would have written.
	cfgPath, err := IronProxyConfigPath(identity.Prod, projectID)
	require.NoError(t, err)
	require.NoError(t, writeIronProxyConfig(cfgPath, IronProxyConfig{
		HTTPListen:  "127.0.0.1:9080",
		HTTPSListen: "127.0.0.1:9443",
		DNSListen:   "127.0.0.1:9053",
	}))

	// Snapshot-shaped schema.Config: one allow entry carrying a
	// per-host secret scope.
	snapCfg := schema.Config{
		Project: schema.Project{Name: projectID},
		Env:     map[string]schema.EnvValue{"TOKEN": {Secret: &schema.SecretRef{Name: "gh"}}},
		Network: schema.Network{
			Allow: []schema.AllowEntry{
				{Host: "api.github.com", Secrets: []string{"gh"}},
			},
		},
	}

	// Seed the secret in the file-backed store the daemon reads.
	be := secret.NewFileBackend(identity.Prod.SecretsDir())
	require.NoError(t, be.Set(projectID+"/gh", "resolved-value"))

	got, err := rebuildIronProxyConfig(identity.Prod, projectID, snapCfg)
	require.NoError(t, err)

	assert.Equal(t, "127.0.0.1:9080", got.HTTPListen)
	assert.Equal(t, "127.0.0.1:9443", got.HTTPSListen)
	assert.Equal(t, "127.0.0.1:9053", got.DNSListen)
	assert.Equal(t, []string{"api.github.com"}, got.AllowList)
	require.Len(t, got.Secrets, 1)
	assert.Equal(t, "gh", got.Secrets[0].Name)
	assert.Equal(t, "resolved-value", got.Secrets[0].Value)
	assert.Equal(t, []string{"api.github.com"}, got.Secrets[0].Hosts)
}

// TestRebuildIronProxyConfig_DockerExpandsAllowlist locks in that the
// allowlist derivation is docker.EffectiveAllowlist(snapCfg) — not a
// bare Network.Domains() read — so a rebuilt config for a docker:true
// project still carries the Docker Hub hosts.
func TestRebuildIronProxyConfig_DockerExpandsAllowlist(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const projectID = "rebuild-docker-test"

	cfgPath, err := IronProxyConfigPath(identity.Prod, projectID)
	require.NoError(t, err)
	require.NoError(t, writeIronProxyConfig(cfgPath, IronProxyConfig{
		HTTPListen:  "127.0.0.1:9180",
		HTTPSListen: "127.0.0.1:9543",
		DNSListen:   "127.0.0.1:9153",
	}))

	snapCfg := schema.Config{
		Project: schema.Project{Name: projectID},
		Docker:  true,
		Network: schema.Network{
			Allow: []schema.AllowEntry{{Host: "example.com"}},
		},
	}

	got, err := rebuildIronProxyConfig(identity.Prod, projectID, snapCfg)
	require.NoError(t, err)

	assert.Equal(t, []string{
		"example.com",
		"registry-1.docker.io",
		"auth.docker.io",
		"production.cloudfront.docker.com",
	}, got.AllowList)
}
