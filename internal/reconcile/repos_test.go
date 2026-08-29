package reconcile

import (
	"testing"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func strPtr(s string) *string { return &s }
func boolPtr(b bool) *bool    { return &b }

func TestComputeRepoChanges_NoChange(t *testing.T) {
	old := schema.Config{Repos: map[string]schema.RepoConfig{
		"main": {URL: strPtr("git@example.com:a/b.git"), Secret: "gh"},
	}}
	new := old
	assert.Empty(t, computeRepoChanges(old, new))
}

func TestComputeRepoChanges_Add(t *testing.T) {
	old := schema.Config{}
	new := schema.Config{Repos: map[string]schema.RepoConfig{
		"main": {URL: strPtr("git@example.com:a/b.git"), Secret: "gh"},
	}}
	changes := computeRepoChanges(old, new)
	require.Len(t, changes, 1)
	assert.Equal(t, KindRepoChange, changes[0].Kind)
	assert.Equal(t, OpAdd, changes[0].Op)
	assert.Equal(t, "main", changes[0].Key)
	assert.Equal(t, BucketLive, changes[0].Bucket())
}

func TestComputeRepoChanges_Remove(t *testing.T) {
	old := schema.Config{Repos: map[string]schema.RepoConfig{
		"main": {URL: strPtr("git@example.com:a/b.git"), Secret: "gh"},
	}}
	new := schema.Config{}
	changes := computeRepoChanges(old, new)
	require.Len(t, changes, 1)
	assert.Equal(t, KindRepoChange, changes[0].Kind)
	assert.Equal(t, OpRemove, changes[0].Op)
	assert.Equal(t, "main", changes[0].Key)
	assert.Equal(t, BucketLive, changes[0].Bucket())
}

func TestComputeRepoChanges_URL_Mutate(t *testing.T) {
	old := schema.Config{Repos: map[string]schema.RepoConfig{
		"main": {URL: strPtr("git@example.com:a/b.git"), Secret: "gh"},
	}}
	new := schema.Config{Repos: map[string]schema.RepoConfig{
		"main": {URL: strPtr("git@example.com:a/c.git"), Secret: "gh"},
	}}
	changes := computeRepoChanges(old, new)
	require.Len(t, changes, 1)
	assert.Equal(t, KindRepoChange, changes[0].Kind)
	assert.Equal(t, OpMutate, changes[0].Op)
	assert.Equal(t, "main", changes[0].Key)
	assert.Equal(t, "URL", changes[0].Field)
	assert.Equal(t, BucketRestartVM, changes[0].Bucket())
}

func TestComputeRepoChanges_Secret_Mutate(t *testing.T) {
	old := schema.Config{Repos: map[string]schema.RepoConfig{
		"main": {URL: strPtr("git@example.com:a/b.git"), Secret: "gh"},
	}}
	new := schema.Config{Repos: map[string]schema.RepoConfig{
		"main": {URL: strPtr("git@example.com:a/b.git"), Secret: "gh2"},
	}}
	changes := computeRepoChanges(old, new)
	require.Len(t, changes, 1)
	assert.Equal(t, OpMutate, changes[0].Op)
	assert.Equal(t, "Secret", changes[0].Field)
	assert.Equal(t, BucketRestartVM, changes[0].Bucket())
}

func TestComputeRepoChanges_Label_Mutate(t *testing.T) {
	old := schema.Config{Repos: map[string]schema.RepoConfig{
		"main": {URL: strPtr("u"), Label: strPtr("old-label")},
	}}
	new := schema.Config{Repos: map[string]schema.RepoConfig{
		"main": {URL: strPtr("u"), Label: strPtr("new-label")},
	}}
	changes := computeRepoChanges(old, new)
	require.Len(t, changes, 1)
	assert.Equal(t, OpMutate, changes[0].Op)
	assert.Equal(t, "Label", changes[0].Field)
	assert.Equal(t, BucketLive, changes[0].Bucket())
}

func TestComputeRepoChanges_Ignore_Mutate(t *testing.T) {
	old := schema.Config{Repos: map[string]schema.RepoConfig{
		"main": {URL: strPtr("u"), Ignore: []string{"a/"}},
	}}
	new := schema.Config{Repos: map[string]schema.RepoConfig{
		"main": {URL: strPtr("u"), Ignore: []string{"b/"}},
	}}
	changes := computeRepoChanges(old, new)
	require.Len(t, changes, 1)
	assert.Equal(t, OpMutate, changes[0].Op)
	assert.Equal(t, "Ignore", changes[0].Field)
	assert.Equal(t, BucketLive, changes[0].Bucket())
}

func TestComputeRepoChanges_Volume_Toggle(t *testing.T) {
	old := schema.Config{Repos: map[string]schema.RepoConfig{
		"docs": {URL: strPtr("u"), Volume: boolPtr(true)},
	}}
	new := schema.Config{Repos: map[string]schema.RepoConfig{
		"docs": {URL: strPtr("u"), Volume: boolPtr(false)},
	}}
	changes := computeRepoChanges(old, new)
	require.Len(t, changes, 1)
	assert.Equal(t, OpMutate, changes[0].Op)
	assert.Equal(t, "Volume", changes[0].Field)
	assert.Equal(t, BucketLive, changes[0].Bucket())
}

func TestComputeRepoChanges_Primary_Toggle(t *testing.T) {
	old := schema.Config{Repos: map[string]schema.RepoConfig{
		"main": {URL: strPtr("u"), Primary: boolPtr(true)},
	}}
	new := schema.Config{Repos: map[string]schema.RepoConfig{
		"main": {URL: strPtr("u"), Primary: boolPtr(false)},
	}}
	changes := computeRepoChanges(old, new)
	require.Len(t, changes, 1)
	assert.Equal(t, OpMutate, changes[0].Op)
	assert.Equal(t, "Primary", changes[0].Field)
	assert.Equal(t, BucketLive, changes[0].Bucket())
}

func TestComputeRepoChanges_MultipleFieldsSameRepo(t *testing.T) {
	old := schema.Config{Repos: map[string]schema.RepoConfig{
		"main": {URL: strPtr("u1"), Secret: "s1"},
	}}
	new := schema.Config{Repos: map[string]schema.RepoConfig{
		"main": {URL: strPtr("u2"), Secret: "s2"},
	}}
	changes := computeRepoChanges(old, new)
	assert.Len(t, changes, 2)
}

func TestComputeRepoChanges_MultipleSortedDeterministic(t *testing.T) {
	old := schema.Config{Repos: map[string]schema.RepoConfig{"a": {URL: strPtr("a1")}}}
	new := schema.Config{Repos: map[string]schema.RepoConfig{
		"c": {URL: strPtr("c1")},
		"b": {URL: strPtr("b1")},
		"a": {URL: strPtr("a2")},
	}}
	changes := computeRepoChanges(old, new)
	require.Len(t, changes, 3)
	assert.Equal(t, "a", changes[0].Key)
	assert.Equal(t, "b", changes[1].Key)
	assert.Equal(t, "c", changes[2].Key)
}

func TestKindRepoChange_BucketLiveByDefault(t *testing.T) {
	assert.Equal(t, BucketLive, KindRepoChange.Bucket())
}
