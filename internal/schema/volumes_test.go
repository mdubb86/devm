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
		Volumes: map[string]string{
			"postgres-data": "/var/lib/postgresql/data",
			"claude-cache":  "/home/devm/.cache/claude",
		},
	}
	require.NoError(t, c.Validate())
}

func TestVolumes_RejectsEmptyName(t *testing.T) {
	c := Config{
		Project: Project{Name: "p"},
		Volumes: map[string]string{"": "/data"},
	}
	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "volumes: name must not be empty")
}

func TestVolumes_RejectsEmptyPath(t *testing.T) {
	c := Config{
		Project: Project{Name: "p"},
		Volumes: map[string]string{"data": ""},
	}
	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `volumes.data: guest path must not be empty`)
}

func TestVolumes_RejectsRelativePath(t *testing.T) {
	c := Config{
		Project: Project{Name: "p"},
		Volumes: map[string]string{"data": "var/lib/data"},
	}
	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `volumes.data: guest path "var/lib/data" must be absolute`)
}

func TestVolumes_RejectsPathTraversal(t *testing.T) {
	c := Config{
		Project: Project{Name: "p"},
		Volumes: map[string]string{"data": "/var/../etc"},
	}
	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `volumes.data: guest path "/var/../etc" must not contain ..`)
}

func TestVolumes_RejectsDuplicateTargetPaths(t *testing.T) {
	c := Config{
		Project: Project{Name: "p"},
		Volumes: map[string]string{
			"a": "/data",
			"b": "/data",
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
		Volumes: map[string]string{"Bad Name!": "/data"},
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
		Volumes: map[string]string{
			"clash": "/Users/michael/workspace/p/data",
		},
	}
	err := c.ValidateWithRoot("/Users/michael/workspace/p")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `volumes.clash: guest path "/Users/michael/workspace/p/data" overlaps the workspace mount root "/Users/michael/workspace/p"`)
}

func TestVolumes_RejectsOverlapWithMask(t *testing.T) {
	// A mask target lives under the workspace root; volume target is
	// absolute. This test proves the validator flags a volume whose
	// guest path is *identical* to any service's mask *absolute-form*
	// path (repoRoot + "/" + mask.Path). Since ValidateWithRoot is the
	// entry point that knows the workspace root, this test uses it.
	c := Config{
		Project: Project{Name: "p"},
		Services: map[string]Service{
			"api": {
				Port:  8080,
				Masks: []Mask{{Path: "node_modules", Size: "1g"}},
			},
		},
		Volumes: map[string]string{
			"cache": "/Users/michael/workspace/p/node_modules",
		},
	}
	err := c.ValidateWithRoot("/Users/michael/workspace/p")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `volumes.cache: guest path "/Users/michael/workspace/p/node_modules" overlaps mask "node_modules" (service "api")`)
}

func TestVolumes_YAMLRoundTrip(t *testing.T) {
	// Value type must be a plain string, not a nested map — the spec
	// commits to `name: /path` shape, not `name: {target: /path}`.
	yamlIn := "project:\n  name: p\nvolumes:\n  pg: /var/lib/pg\n"
	var c Config
	require.NoError(t, unmarshalYAML([]byte(yamlIn), &c))
	assert.Equal(t, "/var/lib/pg", c.Volumes["pg"])
}
