package reconcile

import (
	"testing"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiff_CommandsAdded(t *testing.T) {
	old := schema.Config{Repos: map[string]schema.RepoConfig{
		"main": {Label: strPtr("work"), Secret: "gh"},
	}}
	new := schema.Config{Repos: map[string]schema.RepoConfig{
		"main": {
			Label: strPtr("work"), Secret: "gh",
			Commands: []string{"install"},
		},
	}}
	changes := computeCommandsChanges(old, new)
	require.Len(t, changes, 1)
	assert.Equal(t, KindCommandsChange, changes[0].Kind)
	assert.Equal(t, OpAdd, changes[0].Op)
	assert.Equal(t, BucketLive, changes[0].Bucket())
	assert.Equal(t, "main", changes[0].Repo)
	assert.Equal(t, "install", changes[0].Key)
}

func TestDiff_CommandsRemoved(t *testing.T) {
	old := schema.Config{Repos: map[string]schema.RepoConfig{
		"main": {
			Label: strPtr("work"), Secret: "gh",
			Commands: []string{"install"},
		},
	}}
	new := schema.Config{Repos: map[string]schema.RepoConfig{
		"main": {Label: strPtr("work"), Secret: "gh"},
	}}
	changes := computeCommandsChanges(old, new)
	require.Len(t, changes, 1)
	assert.Equal(t, KindCommandsChange, changes[0].Kind)
	assert.Equal(t, OpRemove, changes[0].Op)
	assert.Equal(t, BucketLive, changes[0].Bucket())
	assert.Equal(t, "main", changes[0].Repo)
	assert.Equal(t, "install", changes[0].Key)
}

func TestDiff_CommandsNoChange(t *testing.T) {
	cfg := schema.Config{Repos: map[string]schema.RepoConfig{
		"main": {Commands: []string{"install"}},
	}}
	assert.Empty(t, computeCommandsChanges(cfg, cfg))
}

func TestDiff_CommandsSortedDeterministic(t *testing.T) {
	old := schema.Config{}
	new := schema.Config{Repos: map[string]schema.RepoConfig{
		"zeta":  {Commands: []string{"lint"}},
		"alpha": {Commands: []string{"test", "install"}},
	}}
	changes := computeCommandsChanges(old, new)
	require.Len(t, changes, 3)
	assert.Equal(t, "alpha", changes[0].Repo)
	assert.Equal(t, "install", changes[0].Key)
	assert.Equal(t, "alpha", changes[1].Repo)
	assert.Equal(t, "test", changes[1].Key)
	assert.Equal(t, "zeta", changes[2].Repo)
	assert.Equal(t, "lint", changes[2].Key)
}

func TestKindCommandsChange_BucketLive(t *testing.T) {
	assert.Equal(t, BucketLive, KindCommandsChange.Bucket())
}

func TestComputeAllChanges_IncludesCommands(t *testing.T) {
	old := schema.Config{Repos: map[string]schema.RepoConfig{
		"main": {Secret: "gh"},
	}}
	new := schema.Config{Repos: map[string]schema.RepoConfig{
		"main": {
			Secret:   "gh",
			Commands: []string{"install"},
		},
	}}
	changes, err := ComputeAllChanges(old, new, t.TempDir(), t.TempDir(), nil, nil, nil)
	require.NoError(t, err)
	found := false
	for _, c := range changes {
		if c.Kind == KindCommandsChange {
			found = true
			assert.Equal(t, BucketLive, c.Bucket())
		}
	}
	assert.True(t, found, "expected KindCommandsChange in ComputeAllChanges output")
}
