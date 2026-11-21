package render

import (
	"encoding/json"
	"testing"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func p[T any](v T) *T { return &v }

func TestRenderCommandsManifest_Shape(t *testing.T) {
	cfg := schema.Config{
		Project: schema.Project{Name: "p"},
		Repos: map[string]schema.RepoConfig{
			"main": {
				Label:    p("work"),
				Secret:   "gh",
				Commands: []string{"install", "gsd", "lint"},
			},
			"v1": {
				URL:      p("https://example/v1.git"),
				Label:    p("v1"),
				Commands: []string{"seed"},
			},
		},
	}
	body, err := RenderCommandsManifest(cfg, "/host/cwd")
	require.NoError(t, err)

	// Structural round-trip.
	var got struct {
		Repos map[string]struct {
			GuestPath string   `json:"guestPath"`
			Commands  []string `json:"commands"`
		} `json:"repos"`
	}
	require.NoError(t, json.Unmarshal(body, &got))
	assert.Equal(t, "/home/devm/work", got.Repos["main"].GuestPath)
	assert.ElementsMatch(t, []string{"install", "gsd", "lint"}, got.Repos["main"].Commands)

	assert.Equal(t, "/home/devm/v1", got.Repos["v1"].GuestPath)
	assert.Equal(t, []string{"seed"}, got.Repos["v1"].Commands)
}

func TestRenderCommandsManifest_EmptyWhenNoCommands(t *testing.T) {
	// A declared repo with no commands must still appear in the
	// manifest (with commands: []) — omitting it made cmd/run's
	// lookup report "not inside a registered repo" for a cwd that IS in
	// a devm repo, just one with no commands defined.
	cfg := schema.Config{
		Project: schema.Project{Name: "p"},
		Repos: map[string]schema.RepoConfig{
			"main": {Label: p("work"), Secret: "gh"},
		},
	}
	body, err := RenderCommandsManifest(cfg, "/host")
	require.NoError(t, err)
	assert.JSONEq(t, `{"repos":{"main":{"guestPath":"/home/devm/work","commands":[]}}}`, string(body))
}

func TestRenderCommandsManifest_Deterministic(t *testing.T) {
	cfg := schema.Config{
		Project: schema.Project{Name: "p"},
		Repos: map[string]schema.RepoConfig{
			"a": {Label: p("a"), Secret: "gh", Commands: []string{"x", "y"}},
			"b": {Label: p("b"), Secret: "gh", Commands: []string{"z"}},
		},
	}
	first, err := RenderCommandsManifest(cfg, "/h")
	require.NoError(t, err)
	for i := 0; i < 5; i++ {
		again, err := RenderCommandsManifest(cfg, "/h")
		require.NoError(t, err)
		assert.Equal(t, first, again, "manifest must be byte-identical across runs")
	}
}
