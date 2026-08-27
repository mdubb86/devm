package schema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

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
