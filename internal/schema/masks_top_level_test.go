package schema

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMasks_ValidShape(t *testing.T) {
	c := Config{
		Project: Project{Name: "p"},
		Masks:   []string{"node_modules", "companion/.venv"},
	}
	require.NoError(t, c.Validate())
}

func TestMasks_RejectsEmptyEntry(t *testing.T) {
	c := Config{
		Project: Project{Name: "p"},
		Masks:   []string{""},
	}
	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "masks[0]: path must not be empty")
}

func TestMasks_RejectsAbsolutePath(t *testing.T) {
	c := Config{
		Project: Project{Name: "p"},
		Masks:   []string{"/etc/passwd"},
	}
	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `masks[0]: path "/etc/passwd" must be relative to the workspace (no leading /, ~, or $)`)
}

func TestMasks_RejectsTildePath(t *testing.T) {
	c := Config{
		Project: Project{Name: "p"},
		Masks:   []string{"~/scratch"},
	}
	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `masks[0]: path "~/scratch" must be relative to the workspace (no leading /, ~, or $)`)
}

func TestMasks_RejectsDollarPath(t *testing.T) {
	c := Config{
		Project: Project{Name: "p"},
		Masks:   []string{"$HOME/scratch"},
	}
	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `masks[0]: path "$HOME/scratch" must be relative to the workspace (no leading /, ~, or $)`)
}

func TestMasks_RejectsPathTraversal(t *testing.T) {
	c := Config{
		Project: Project{Name: "p"},
		Masks:   []string{"../escape"},
	}
	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `masks[0]: path "../escape": path traversal outside the workspace is not allowed`)
}

func TestMasks_RejectsDuplicates(t *testing.T) {
	c := Config{
		Project: Project{Name: "p"},
		Masks:   []string{"node_modules", "node_modules"},
	}
	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `masks[1]: path "node_modules" is already declared`)
}

func TestMasks_RejectsOverlapWithVolume(t *testing.T) {
	// A mask target overlaps a declared volume when
	// filepath.Join(workspaceRoot, maskPath) == volume guest path.
	c := Config{
		Project: Project{Name: "p"},
		Masks:   []string{"data"},
		Volumes: map[string]string{
			"clash": "/Users/michael/workspace/p/data",
		},
	}
	err := c.ValidateWithRoot("/Users/michael/workspace/p")
	require.Error(t, err)
	// Either side's overlap check may fire first depending on Config.Validate ordering;
	// accept either message as long as it names the overlapping pair.
	msg := err.Error()
	assert.True(t,
		strings.Contains(msg, `volumes.clash: guest path "/Users/michael/workspace/p/data" overlaps mask "data"`) ||
			strings.Contains(msg, `masks[0]: path "data" overlaps volume "clash"`),
		"unexpected message: %s", msg,
	)
}

func TestMasks_YAMLRoundTrip(t *testing.T) {
	// Value type must be a plain []string — spec commits to
	// `masks: [path, path]` shape, not a map or list-of-maps.
	yamlIn := "project:\n  name: p\nmasks:\n  - node_modules\n  - companion/.venv\n"
	var c Config
	require.NoError(t, unmarshalYAML([]byte(yamlIn), &c))
	assert.Equal(t, []string{"node_modules", "companion/.venv"}, c.Masks)
}

// Schema-md drift check for the new `masks` field is enforced by
// internal/skills/drift_test.go's TestSchemaSkillMentionsAllConfigFields,
// which walks Config's yaml tags. No test needed here.
