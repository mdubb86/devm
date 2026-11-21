package schema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestOrderedMap_PreservesYAMLOrder(t *testing.T) {
	var m OrderedMap[int]
	require.NoError(t, yaml.Unmarshal([]byte("z: 1\na: 2\nm: 3\n"), &m))
	assert.Equal(t, []string{"z", "a", "m"}, m.Order)
	assert.Equal(t, map[string]int{"z": 1, "a": 2, "m": 3}, m.Entries)
}

func TestOrderedMap_IterWalksOrder(t *testing.T) {
	var m OrderedMap[int]
	require.NoError(t, yaml.Unmarshal([]byte("z: 1\na: 2\nm: 3\n"), &m))
	var seen []string
	for k, v := range m.Iter() {
		seen = append(seen, k)
		assert.Equal(t, m.Entries[k], v)
	}
	assert.Equal(t, []string{"z", "a", "m"}, seen)
}

func TestOrderedMap_EmptyMapIsNilSafe(t *testing.T) {
	var m OrderedMap[int]
	assert.Equal(t, 0, m.Len())
	_, ok := m.Get("missing")
	assert.False(t, ok)
	var seen []string
	for k := range m.Iter() {
		seen = append(seen, k)
	}
	assert.Nil(t, seen)
}

func TestOrderedMap_DuplicateKeyIsError(t *testing.T) {
	var m OrderedMap[int]
	err := yaml.Unmarshal([]byte("z: 1\nz: 2\n"), &m)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"z"`)
}

func TestOrderedMap_MarshalRoundTripPreservesOrder(t *testing.T) {
	var m OrderedMap[int]
	m.Set("z", 1)
	m.Set("a", 2)
	m.Set("m", 3)

	out, err := yaml.Marshal(&m)
	require.NoError(t, err)

	var round OrderedMap[int]
	require.NoError(t, yaml.Unmarshal(out, &round))
	assert.Equal(t, []string{"z", "a", "m"}, round.Order)
	assert.Equal(t, m.Entries, round.Entries)
}
