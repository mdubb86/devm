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
`
	var got map[string]Volume
	require.NoError(t, yaml.Unmarshal([]byte(src), &got))
	v := got["vendor-lib"]
	require.NotNil(t, v.Repo)
	assert.Equal(t, "vendor_token", v.Repo.Secret)
}

func TestVolume_MarshalUnmarshalRoundTrip_WithRepo(t *testing.T) {
	url := "https://github.com/me/foo.git"
	label := "foo"
	orig := map[string]Volume{
		"primary": {
			Path: "/home/devm/workspace/foo",
			Repo: &RepoConfig{
				URL:    &url,
				Secret: "gh_token",
				Label:  &label,
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
	require.NotNil(t, round["primary"].Repo.Label)
	assert.Equal(t, label, *round["primary"].Repo.Label)
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

func TestRepoConfig_KnownFields_NewShape(t *testing.T) {
	assert.ElementsMatch(t,
		[]string{"url", "secret", "label", "volume", "primary", "ignore"},
		repoKnownFields,
	)
}

func TestRepoConfig_UnmarshalYAML_FullShape(t *testing.T) {
	y := `url: git@github.com:me/foo.git
secret: github
label: myproject
volume: true
primary: true
ignore:
  - node_modules
  - .venv
`
	var r RepoConfig
	require.NoError(t, yaml.Unmarshal([]byte(y), &r))
	require.NotNil(t, r.URL)
	assert.Equal(t, "git@github.com:me/foo.git", *r.URL)
	assert.Equal(t, "github", r.Secret)
	require.NotNil(t, r.Label)
	assert.Equal(t, "myproject", *r.Label)
	require.NotNil(t, r.Volume)
	assert.True(t, *r.Volume)
	require.NotNil(t, r.Primary)
	assert.True(t, *r.Primary)
	assert.Equal(t, []string{"node_modules", ".venv"}, r.Ignore)
}

func TestRepoConfig_UnmarshalYAML_RejectsBranch(t *testing.T) {
	var r RepoConfig
	err := yaml.Unmarshal([]byte("branch: main\nsecret: github\n"), &r)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown field \"branch\"")
}

func TestRepoConfig_UnmarshalYAML_RejectsUnknown(t *testing.T) {
	var r RepoConfig
	err := yaml.Unmarshal([]byte("wat: yes\nsecret: github\n"), &r)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown field \"wat\"")
}
