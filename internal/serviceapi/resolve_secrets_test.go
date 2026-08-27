package serviceapi

import (
	"testing"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/secret"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func secretRef(name string) schema.EnvValue {
	return schema.EnvValue{Secret: &schema.SecretRef{Name: name}}
}

func TestResolveSecretBindings(t *testing.T) {
	t.Run("secret_with_host_scope", func(t *testing.T) {
		// A secret named under a host allow-entry comes back with Hosts populated.
		be := secret.NewFake()
		require.NoError(t, be.Set("proj/gh", "token123"))

		cfg := schema.Config{
			Project: schema.Project{Name: "proj"},
			Env:     map[string]schema.EnvValue{"TOKEN": secretRef("gh")},
			Network: schema.Network{
				Allow: []schema.AllowEntry{
					{Host: "api.github.com", Secrets: []string{"gh"}},
				},
			},
		}

		bindings, err := ResolveSecretBindings(cfg, be, "")
		require.NoError(t, err)
		require.Len(t, bindings, 1)
		assert.Equal(t, "gh", bindings[0].Name)
		assert.Equal(t, "token123", bindings[0].Value)
		assert.Equal(t, []string{"api.github.com"}, bindings[0].Hosts)
	})

	t.Run("secret_with_no_host_scope", func(t *testing.T) {
		// A secret referenced in env but bound to NO allow-entry host comes
		// back with empty/nil Hosts (iron-proxy never injects it).
		be := secret.NewFake()
		require.NoError(t, be.Set("proj/mytoken", "secret_value"))

		cfg := schema.Config{
			Project: schema.Project{Name: "proj"},
			Env:     map[string]schema.EnvValue{"MY_TOKEN": secretRef("mytoken")},
			Network: schema.Network{
				Allow: []schema.AllowEntry{
					{Host: "example.com"}, // no secrets listed
				},
			},
		}

		bindings, err := ResolveSecretBindings(cfg, be, "")
		require.NoError(t, err)
		require.Len(t, bindings, 1)
		assert.Equal(t, "mytoken", bindings[0].Name)
		assert.Equal(t, "secret_value", bindings[0].Value)
		assert.Empty(t, bindings[0].Hosts)
	})

	t.Run("missing_store_entry_returns_error", func(t *testing.T) {
		// A !secret whose file-store entry is missing → error mentioning the name.
		be := secret.NewFake()
		// Deliberately do NOT seed "proj/missing".

		cfg := schema.Config{
			Project: schema.Project{Name: "proj"},
			Env:     map[string]schema.EnvValue{"TOKEN": secretRef("missing")},
		}

		_, err := ResolveSecretBindings(cfg, be, "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing")
	})

	t.Run("secret_under_two_hosts_gets_both_sorted", func(t *testing.T) {
		// A secret named under two allow entries comes back with both hosts sorted.
		be := secret.NewFake()
		require.NoError(t, be.Set("proj/tok", "val"))

		cfg := schema.Config{
			Project: schema.Project{Name: "proj"},
			Env:     map[string]schema.EnvValue{"T": secretRef("tok")},
			Network: schema.Network{
				Allow: []schema.AllowEntry{
					{Host: "z.example.com", Secrets: []string{"tok"}},
					{Host: "a.example.com", Secrets: []string{"tok"}},
				},
			},
		}

		bindings, err := ResolveSecretBindings(cfg, be, "")
		require.NoError(t, err)
		require.Len(t, bindings, 1)
		assert.Equal(t, []string{"a.example.com", "z.example.com"}, bindings[0].Hosts)
	})

	t.Run("no_secrets_returns_nil", func(t *testing.T) {
		be := secret.NewFake()
		cfg := schema.Config{
			Project: schema.Project{Name: "proj"},
			Env:     map[string]schema.EnvValue{"PLAIN": {Literal: "value"}},
		}
		bindings, err := ResolveSecretBindings(cfg, be, "")
		require.NoError(t, err)
		assert.Nil(t, bindings)
	})

}

func TestRepoHosts(t *testing.T) {
	t.Run("file_url_host_empty_not_literal_file", func(t *testing.T) {
		host, err := repoURLHost("file:///tmp/foo")
		require.NoError(t, err)
		assert.Equal(t, "", host)
	})

	t.Run("no_repo_declarations_returns_empty", func(t *testing.T) {
		cfg := schema.Config{Project: schema.Project{Name: "proj"}}
		hosts, err := RepoHosts(cfg, "")
		require.NoError(t, err)
		assert.Empty(t, hosts)
	})
}
