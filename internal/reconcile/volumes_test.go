package reconcile

import (
	"testing"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
)

func TestComputeVolumeChanges_NoChange(t *testing.T) {
	old := schema.Config{Volumes: map[string]schema.Volume{"pg": {Path: "/data"}}}
	new := schema.Config{Volumes: map[string]schema.Volume{"pg": {Path: "/data"}}}
	assert.Empty(t, computeVolumeChanges(old, new))
}

func TestComputeVolumeChanges_Add(t *testing.T) {
	old := schema.Config{}
	new := schema.Config{Volumes: map[string]schema.Volume{"pg": {Path: "/data"}}}
	changes := computeVolumeChanges(old, new)
	assert.Len(t, changes, 1)
	assert.Equal(t, KindVolumeChange, changes[0].Kind)
	assert.Equal(t, "pg", changes[0].Key)
	assert.Equal(t, "", changes[0].Old)
	assert.Equal(t, "/data", changes[0].New)
}

func TestComputeVolumeChanges_Remove(t *testing.T) {
	old := schema.Config{Volumes: map[string]schema.Volume{"pg": {Path: "/data"}}}
	new := schema.Config{}
	changes := computeVolumeChanges(old, new)
	assert.Len(t, changes, 1)
	assert.Equal(t, KindVolumeChange, changes[0].Kind)
	assert.Equal(t, "pg", changes[0].Key)
	assert.Equal(t, "/data", changes[0].Old)
	assert.Equal(t, "", changes[0].New)
}

func TestComputeVolumeChanges_Retarget(t *testing.T) {
	old := schema.Config{Volumes: map[string]schema.Volume{"pg": {Path: "/data"}}}
	new := schema.Config{Volumes: map[string]schema.Volume{"pg": {Path: "/var/lib/postgresql/data"}}}
	changes := computeVolumeChanges(old, new)
	assert.Len(t, changes, 1)
	assert.Equal(t, KindVolumeChange, changes[0].Kind)
	assert.Equal(t, "pg", changes[0].Key)
	assert.Equal(t, "/data", changes[0].Old)
	assert.Equal(t, "/var/lib/postgresql/data", changes[0].New)
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
	assert.Len(t, changes, 3)
	// Sorted by name: a (retarget), b (add), c (add).
	assert.Equal(t, "a", changes[0].Key)
	assert.Equal(t, "b", changes[1].Key)
	assert.Equal(t, "c", changes[2].Key)
}

func TestKindVolumeChange_BucketRestartVM(t *testing.T) {
	assert.Equal(t, BucketRestartVM, KindVolumeChange.Bucket())
}
