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
		Scripts: map[string][]string{
			"gsd": {"npx foo", "npx bar"},
		},
		Repos: map[string]schema.RepoConfig{
			"main": {
				Label:  p("work"),
				Secret: "gh",
				Commands: map[string]schema.RepoCommand{
					"install": {Exec: "pnpm install", Startup: p(true)},
					"gsd":     {Exec: ">gsd", Startup: p(true)},
					"lint":    {Exec: "pnpm lint"},
				},
			},
			"v1": {
				URL:   p("https://example/v1.git"),
				Label: p("v1"),
				Commands: map[string]schema.RepoCommand{
					"seed": {Exec: "python seed.py"},
				},
			},
		},
	}
	body, err := RenderCommandsManifest(cfg, "/host/cwd")
	require.NoError(t, err)

	// Structural round-trip.
	var got struct {
		Repos map[string]struct {
			GuestPath string `json:"guestPath"`
			Commands  map[string]struct {
				Exec    string `json:"exec"`
				Startup bool   `json:"startup"`
			} `json:"commands"`
		} `json:"repos"`
	}
	require.NoError(t, json.Unmarshal(body, &got))
	assert.Equal(t, "/home/devm/work", got.Repos["main"].GuestPath)
	assert.Equal(t, "pnpm install", got.Repos["main"].Commands["install"].Exec)
	assert.True(t, got.Repos["main"].Commands["install"].Startup)
	assert.Equal(t, "npx foo && npx bar", got.Repos["main"].Commands["gsd"].Exec,
		"script refs must be resolved at render time")
	assert.False(t, got.Repos["main"].Commands["lint"].Startup, "unspecified startup renders as false")

	assert.Equal(t, "/home/devm/v1", got.Repos["v1"].GuestPath)
	assert.Equal(t, "python seed.py", got.Repos["v1"].Commands["seed"].Exec)
}

func TestRenderCommandsManifest_EmptyWhenNoCommands(t *testing.T) {
	// A declared repo with no commands must still appear in the
	// manifest (with commands: {}) — omitting it made cmd/run's
	// lookup report "no devm repo in current directory" for a cwd that
	// IS in a devm repo, just one with no commands defined.
	cfg := schema.Config{
		Project: schema.Project{Name: "p"},
		Repos: map[string]schema.RepoConfig{
			"main": {Label: p("work"), Secret: "gh"},
		},
	}
	body, err := RenderCommandsManifest(cfg, "/host")
	require.NoError(t, err)
	assert.JSONEq(t, `{"repos":{"main":{"guestPath":"/home/devm/work","commands":{}}}}`, string(body))
}

func TestRenderCommandsManifest_Deterministic(t *testing.T) {
	cfg := schema.Config{
		Project: schema.Project{Name: "p"},
		Repos: map[string]schema.RepoConfig{
			"a": {Label: p("a"), Secret: "gh", Commands: map[string]schema.RepoCommand{
				"x": {Exec: "true"}, "y": {Exec: "true"},
			}},
			"b": {Label: p("b"), Secret: "gh", Commands: map[string]schema.RepoCommand{
				"z": {Exec: "true"},
			}},
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
