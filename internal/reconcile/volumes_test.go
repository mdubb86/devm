package reconcile

import (
	"testing"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestComputeVolumeChanges_NoChange(t *testing.T) {
	old := schema.Config{Volumes: map[string]schema.Volume{"pg": {Path: "/data"}}}
	new := schema.Config{Volumes: map[string]schema.Volume{"pg": {Path: "/data"}}}
	assert.Empty(t, computeVolumeChanges(old, new))
}

func TestComputeVolumeChanges_Add(t *testing.T) {
	old := schema.Config{}
	new := schema.Config{Volumes: map[string]schema.Volume{"claude": {Path: "/home/devm/.claude"}}}
	changes := computeVolumeChanges(old, new)
	require.Len(t, changes, 1)
	assert.Equal(t, KindVolumeChange, changes[0].Kind)
	assert.Equal(t, OpAdd, changes[0].Op)
	assert.Equal(t, "claude", changes[0].Key)
	assert.Equal(t, "/home/devm/.claude", changes[0].New)
	assert.Equal(t, BucketLive, changes[0].Bucket())
}

func TestComputeVolumeChanges_Remove(t *testing.T) {
	old := schema.Config{Volumes: map[string]schema.Volume{"pg": {Path: "/data"}}}
	new := schema.Config{}
	changes := computeVolumeChanges(old, new)
	require.Len(t, changes, 1)
	assert.Equal(t, KindVolumeChange, changes[0].Kind)
	assert.Equal(t, OpRemove, changes[0].Op)
	assert.Equal(t, "pg", changes[0].Key)
	assert.Equal(t, "/data", changes[0].Old)
	assert.Equal(t, BucketLive, changes[0].Bucket())
}

func TestComputeVolumeChanges_PathMutate(t *testing.T) {
	old := schema.Config{Volumes: map[string]schema.Volume{"pg": {Path: "/data"}}}
	new := schema.Config{Volumes: map[string]schema.Volume{"pg": {Path: "/var/lib/postgresql/data"}}}
	changes := computeVolumeChanges(old, new)
	require.Len(t, changes, 1)
	assert.Equal(t, KindVolumeChange, changes[0].Kind)
	assert.Equal(t, OpMutate, changes[0].Op)
	assert.Equal(t, "pg", changes[0].Key)
	assert.Equal(t, "path", changes[0].Field)
	assert.Equal(t, "/data", changes[0].Old)
	assert.Equal(t, "/var/lib/postgresql/data", changes[0].New)
	assert.Equal(t, BucketLive, changes[0].Bucket())
}

func TestComputeVolumeChanges_LabelMutate(t *testing.T) {
	oldLabel, newLabel := "old-label", "new-label"
	old := schema.Config{Volumes: map[string]schema.Volume{"pg": {Path: "/data", Label: &oldLabel}}}
	new := schema.Config{Volumes: map[string]schema.Volume{"pg": {Path: "/data", Label: &newLabel}}}
	changes := computeVolumeChanges(old, new)
	require.Len(t, changes, 1)
	assert.Equal(t, KindVolumeChange, changes[0].Kind)
	assert.Equal(t, OpMutate, changes[0].Op)
	assert.Equal(t, "pg", changes[0].Key)
	assert.Equal(t, "label", changes[0].Field)
	assert.Equal(t, BucketLive, changes[0].Bucket())
}

func TestComputeVolumeChanges_IgnoreMutate(t *testing.T) {
	old := schema.Config{Volumes: map[string]schema.Volume{"pg": {Path: "/data", Ignore: []string{"a/"}}}}
	new := schema.Config{Volumes: map[string]schema.Volume{"pg": {Path: "/data", Ignore: []string{"b/"}}}}
	changes := computeVolumeChanges(old, new)
	require.Len(t, changes, 1)
	assert.Equal(t, KindVolumeChange, changes[0].Kind)
	assert.Equal(t, OpMutate, changes[0].Op)
	assert.Equal(t, "pg", changes[0].Key)
	assert.Equal(t, "ignore", changes[0].Field)
	assert.Equal(t, BucketLive, changes[0].Bucket())
}

func TestComputeVolumeChanges_MultipleFieldsSameVolume(t *testing.T) {
	oldLabel, newLabel := "old-label", "new-label"
	old := schema.Config{Volumes: map[string]schema.Volume{"pg": {Path: "/data", Label: &oldLabel}}}
	new := schema.Config{Volumes: map[string]schema.Volume{"pg": {Path: "/data2", Label: &newLabel}}}
	changes := computeVolumeChanges(old, new)
	assert.Len(t, changes, 2)
}

func TestComputeVolumeChanges_MultipleSortedDeterministic(t *testing.T) {
	// Go's map iteration is random; the detector must sort so output
	// order is stable.
	old := schema.Config{Volumes: map[string]schema.Volume{"a": {Path: "/a1"}}}
	new := schema.Config{Volumes: map[string]schema.Volume{
		"c": {Path: "/c1"},
		"b": {Path: "/b1"},
		"a": {Path: "/a2"},
	}}
	changes := computeVolumeChanges(old, new)
	require.Len(t, changes, 3)
	// Sorted by name: a (retarget), b (add), c (add).
	assert.Equal(t, "a", changes[0].Key)
	assert.Equal(t, "b", changes[1].Key)
	assert.Equal(t, "c", changes[2].Key)
}

func TestKindVolumeChange_BucketLive(t *testing.T) {
	assert.Equal(t, BucketLive, KindVolumeChange.Bucket())
}
