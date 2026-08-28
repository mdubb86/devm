package serviceapi

import (
	"os/exec"
	"testing"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/secret"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func secretRef(name string) schema.EnvValue {
	return schema.EnvValue{Secret: &schema.SecretRef{Name: name}}
}

// makeRepoWithOrigin mirrors repohelpers_test.go's fixture — a real git
// repo with an `origin` remote — so DeriveRepoURL has something to find
// when a repos entry's url is nil.
func makeRepoWithOrigin(t *testing.T, origin string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, exec.Command("git", "-C", dir, "init", "-q").Run())
	require.NoError(t, exec.Command("git", "-C", dir, "remote", "add", "origin", origin).Run())
	return dir
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

	t.Run("repo_secret_with_explicit_url", func(t *testing.T) {
		// cfg.Repos["main"].Secret + explicit URL → a binding scoped to
		// the URL's host is emitted, with no network.allow entry at all
		// (a bare repos.<name>.secret: must be sufficient).
		be := secret.NewFake()
		require.NoError(t, be.Set("proj/gh_token", "tok-value"))

		cfg := schema.Config{
			Project: schema.Project{Name: "proj"},
			Repos: map[string]schema.RepoConfig{
				"main": {
					URL:    strPtr("https://github.com/acme/widget.git"),
					Secret: "gh_token",
				},
			},
		}

		bindings, err := ResolveSecretBindings(cfg, be, "")
		require.NoError(t, err)
		require.Len(t, bindings, 1)
		assert.Equal(t, "gh_token", bindings[0].Name)
		assert.Equal(t, "tok-value", bindings[0].Value)
		assert.Equal(t, []string{"github.com"}, bindings[0].Hosts)
	})

	t.Run("repo_nil_url_derives_from_mac_cwd", func(t *testing.T) {
		// A repos entry with url == nil (the primary) falls through to
		// DeriveRepoURL(macCwd).
		be := secret.NewFake()
		require.NoError(t, be.Set("proj/gh_token", "tok-value"))
		dir := makeRepoWithOrigin(t, "https://github.com/acme/derived.git")

		cfg := schema.Config{
			Project: schema.Project{Name: "proj"},
			Repos: map[string]schema.RepoConfig{
				"main": {Secret: "gh_token"},
			},
		}

		bindings, err := ResolveSecretBindings(cfg, be, dir)
		require.NoError(t, err)
		require.Len(t, bindings, 1)
		assert.Equal(t, []string{"github.com"}, bindings[0].Hosts)
	})

	t.Run("repo_nil_url_derive_failure_surfaces", func(t *testing.T) {
		// A macCwd that isn't a git repo (or has no origin) surfaces
		// DeriveRepoURL's error rather than silently dropping the
		// binding.
		be := secret.NewFake()
		require.NoError(t, be.Set("proj/gh_token", "tok-value"))

		cfg := schema.Config{
			Project: schema.Project{Name: "proj"},
			Repos: map[string]schema.RepoConfig{
				"main": {Secret: "gh_token"},
			},
		}

		_, err := ResolveSecretBindings(cfg, be, t.TempDir())
		require.Error(t, err)
	})

	t.Run("repo_with_no_secret_contributes_nothing", func(t *testing.T) {
		// A repos entry with no secret (public repo) is a no-op for
		// injection scope — no error, no binding.
		be := secret.NewFake()

		cfg := schema.Config{
			Project: schema.Project{Name: "proj"},
			Repos: map[string]schema.RepoConfig{
				"main": {URL: strPtr("https://example.com/pub/repo.git")},
			},
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

	t.Run("repo_host_resolved", func(t *testing.T) {
		cfg := schema.Config{
			Project: schema.Project{Name: "proj"},
			Repos: map[string]schema.RepoConfig{
				"main": {
					URL:    strPtr("https://github.com/acme/widget.git"),
					Secret: "gh_token",
				},
			},
		}
		hosts, err := RepoHosts(cfg, "")
		require.NoError(t, err)
		assert.Equal(t, []string{"github.com"}, hosts)
	})

	t.Run("scp_like_ssh_url_host_parsed", func(t *testing.T) {
		cfg := schema.Config{
			Project: schema.Project{Name: "proj"},
			Repos: map[string]schema.RepoConfig{
				"main": {
					URL:    strPtr("git@github.com:acme/widget.git"),
					Secret: "gh_token",
				},
			},
		}
		hosts, err := RepoHosts(cfg, "")
		require.NoError(t, err)
		assert.Equal(t, []string{"github.com"}, hosts)
	})

	t.Run("multiple_repos_hosts_merged_sorted", func(t *testing.T) {
		cfg := schema.Config{
			Project: schema.Project{Name: "proj"},
			Repos: map[string]schema.RepoConfig{
				"main": {
					URL:    strPtr("https://z.example.com/acme/main.git"),
					Secret: "gh_token",
				},
				"secondary": {
					URL:    strPtr("https://a.example.com/acme/second.git"),
					Secret: "other_token",
				},
			},
		}
		hosts, err := RepoHosts(cfg, "")
		require.NoError(t, err)
		assert.Equal(t, []string{"a.example.com", "z.example.com"}, hosts)
	})
}
