package schema

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func unmarshalYAML(data []byte, out interface{}) error {
	return yaml.Unmarshal(data, out)
}

func TestVolumes_ValidShape(t *testing.T) {
	c := Config{
		Project: Project{Name: "p"},
		Volumes: map[string]Volume{
			"postgres-data": {Path: "/var/lib/postgresql/data"},
			"claude-cache":  {Path: "/home/devm/.cache/claude"},
		},
	}
	require.NoError(t, c.Validate())
}

func TestVolumes_RejectsEmptyName(t *testing.T) {
	c := Config{
		Project: Project{Name: "p"},
		Volumes: map[string]Volume{"": {Path: "/data"}},
	}
	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "volumes: name must not be empty")
}

func TestVolumes_RejectsEmptyPath(t *testing.T) {
	c := Config{
		Project: Project{Name: "p"},
		Volumes: map[string]Volume{"data": {Path: ""}},
	}
	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `volumes.data: guest path must not be empty`)
}

func TestVolumes_RejectsRelativePath(t *testing.T) {
	c := Config{
		Project: Project{Name: "p"},
		Volumes: map[string]Volume{"data": {Path: "var/lib/data"}},
	}
	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `volumes.data: guest path "var/lib/data" must be absolute`)
}

func TestVolumes_RejectsPathTraversal(t *testing.T) {
	c := Config{
		Project: Project{Name: "p"},
		Volumes: map[string]Volume{"data": {Path: "/var/../etc"}},
	}
	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `volumes.data: guest path "/var/../etc" must not contain ..`)
}

func TestVolumes_RejectsDuplicateTargetPaths(t *testing.T) {
	c := Config{
		Project: Project{Name: "p"},
		Volumes: map[string]Volume{
			"a": {Path: "/data"},
			"b": {Path: "/data"},
		},
	}
	err := c.Validate()
	require.Error(t, err)
	// Name of the later volume in sorted order surfaces the conflict.
	assert.True(t,
		strings.Contains(err.Error(), `volumes.b: guest path "/data" already declared by volume "a"`) ||
			strings.Contains(err.Error(), `volumes.a: guest path "/data" already declared by volume "b"`),
		"got: %s", err.Error(),
	)
}

func TestVolumes_RejectsInvalidName(t *testing.T) {
	// Names must be [a-z0-9][a-z0-9._-]* — mount tag `vol_<name>` and
	// filesystem path segment both must be safe.
	c := Config{
		Project: Project{Name: "p"},
		Volumes: map[string]Volume{"Bad Name!": {Path: "/data"}},
	}
	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `volumes: name "Bad Name!" must match [a-z0-9][a-z0-9._-]*`)
}

func TestVolumes_RejectsOverlapWithWorkspace(t *testing.T) {
	// The workspace is virtiofs-mounted at the SAME absolute path in
	// the guest as it lives on the Mac (mirrored). A volume declared
	// at that path (or any subpath) would collide.
	c := Config{
		Project: Project{Name: "p"},
		Volumes: map[string]Volume{
			"clash": {Path: "/Users/michael/workspace/p/data"},
		},
	}
	err := c.ValidateWithRoot("/Users/michael/workspace/p")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `volumes.clash: guest path "/Users/michael/workspace/p/data" overlaps the workspace mount root "/Users/michael/workspace/p"`)
}

func TestVolumes_YAMLRoundTrip(t *testing.T) {
	// Bare scalar shape decodes to Volume.Path with Label/Ignore left nil.
	yamlIn := "project:\n  name: p\nvolumes:\n  pg: /var/lib/pg\n"
	var c Config
	require.NoError(t, unmarshalYAML([]byte(yamlIn), &c))
	assert.Equal(t, "/var/lib/pg", c.Volumes["pg"].Path)
	assert.Nil(t, c.Volumes["pg"].Label)
	assert.Nil(t, c.Volumes["pg"].Ignore)
}

func TestVolume_KnownFields_NewShape(t *testing.T) {
	assert.ElementsMatch(t,
		[]string{"path", "label", "ignore"},
		volumeKnownFields,
	)
}

func TestVolume_UnmarshalYAML_Scalar(t *testing.T) {
	var v Volume
	require.NoError(t, yaml.Unmarshal([]byte(`/home/devm/.claude`), &v))
	assert.Equal(t, "/home/devm/.claude", v.Path)
	assert.Nil(t, v.Label)
	assert.Nil(t, v.Ignore)
}

func TestVolume_UnmarshalYAML_FullShape(t *testing.T) {
	y := `path: /home/devm/.claude
label: claude
ignore:
  - Cache/
`
	var v Volume
	require.NoError(t, yaml.Unmarshal([]byte(y), &v))
	assert.Equal(t, "/home/devm/.claude", v.Path)
	require.NotNil(t, v.Label)
	assert.Equal(t, "claude", *v.Label)
	assert.Equal(t, []string{"Cache/"}, v.Ignore)
}

func TestVolume_UnmarshalYAML_RejectsRepoField(t *testing.T) {
	var v Volume
	err := yaml.Unmarshal([]byte("path: /x\nrepo: {url: g}\n"), &v)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown field \"repo\"")
}
