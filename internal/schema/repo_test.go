package schema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestVolume_ScalarShape(t *testing.T) {
	src := `pg-data: /var/lib/postgresql/data`
	var got map[string]Volume
	require.NoError(t, yaml.Unmarshal([]byte(src), &got))
	assert.Equal(t, "/var/lib/postgresql/data", got["pg-data"].Path)
	assert.Nil(t, got["pg-data"].Repo)
}

func TestVolume_MappingShape_RepoInherits(t *testing.T) {
	src := `
design-tokens:
  path: /home/devm/design-tokens
  repo:
    url: https://github.com/me/design-tokens.git
`
	var got map[string]Volume
	require.NoError(t, yaml.Unmarshal([]byte(src), &got))
	v := got["design-tokens"]
	assert.Equal(t, "/home/devm/design-tokens", v.Path)
	require.NotNil(t, v.Repo)
	require.NotNil(t, v.Repo.URL)
	assert.Equal(t, "https://github.com/me/design-tokens.git", *v.Repo.URL)
	assert.Empty(t, v.Repo.Secret) // inherits from top-level Config.Repo.Secret
}

func TestVolume_MappingShape_RepoExplicitSecret(t *testing.T) {
	src := `
vendor-lib:
  path: /home/devm/vendor-lib
  repo:
    url: https://github.com/vendor/lib.git
    secret: vendor_token
    branch: release
`
	var got map[string]Volume
	require.NoError(t, yaml.Unmarshal([]byte(src), &got))
	v := got["vendor-lib"]
	require.NotNil(t, v.Repo)
	assert.Equal(t, "vendor_token", v.Repo.Secret)
	require.NotNil(t, v.Repo.Branch)
	assert.Equal(t, "release", *v.Repo.Branch)
}

func TestVolume_MarshalUnmarshalRoundTrip_WithRepo(t *testing.T) {
	url := "https://github.com/me/foo.git"
	branch := "main"
	orig := map[string]Volume{
		"primary": {
			Path: "/home/devm/workspace/foo",
			Repo: &RepoConfig{
				URL:    &url,
				Secret: "gh_token",
				Branch: &branch,
			},
		},
		"scratch": {Path: "/var/lib/pg"},
	}
	buf, err := yaml.Marshal(orig)
	require.NoError(t, err)

	var round map[string]Volume
	require.NoError(t, yaml.Unmarshal(buf, &round))
	assert.Equal(t, orig["scratch"].Path, round["scratch"].Path)
	assert.Nil(t, round["scratch"].Repo)
	assert.Equal(t, orig["primary"].Path, round["primary"].Path)
	require.NotNil(t, round["primary"].Repo)
	require.NotNil(t, round["primary"].Repo.URL)
	assert.Equal(t, url, *round["primary"].Repo.URL)
	assert.Equal(t, "gh_token", round["primary"].Repo.Secret)
	require.NotNil(t, round["primary"].Repo.Branch)
	assert.Equal(t, branch, *round["primary"].Repo.Branch)
}

func TestVolume_UnknownKey_Rejected(t *testing.T) {
	src := `
foo:
  path: /x
  bogus: 1
`
	var got map[string]Volume
	err := yaml.Unmarshal([]byte(src), &got)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown field")
}

func TestConfig_Repo_MissingSecret(t *testing.T) {
	src := `
project:
  name: p
repo:
  url: https://github.com/x/y.git
`
	var cfg Config
	require.NoError(t, yaml.Unmarshal([]byte(src), &cfg))
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repo.secret is required")
}

func TestConfig_Volume_RepoInheritsFromTopLevel(t *testing.T) {
	src := `
project:
  name: p
repo:
  url: https://github.com/x/primary.git
  secret: gh_token
volumes:
  secondary:
    path: /home/devm/secondary
    repo:
      url: https://github.com/x/secondary.git
`
	var cfg Config
	require.NoError(t, yaml.Unmarshal([]byte(src), &cfg))
	assert.NoError(t, cfg.Validate())
}

func TestConfig_Volume_RepoNoSecretNoTopLevel(t *testing.T) {
	src := `
project:
  name: p
volumes:
  secondary:
    path: /home/devm/secondary
    repo:
      url: https://github.com/x/secondary.git
`
	var cfg Config
	require.NoError(t, yaml.Unmarshal([]byte(src), &cfg))
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no top-level repo.secret to inherit")
}
