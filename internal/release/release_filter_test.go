package release

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFilterTags_OnlyVStarStable(t *testing.T) {
	in := []string{
		"v0.30.0",
		"v0.31.0",
		"recipes-v1.0.0",
		"recipes-abc1234",
		"v1.0.0-rc.1",
		"v2.0.0-beta",
		"v0.31.1",
		"misc-tag",
	}
	got, err := FilterTags(in, false /*includePre*/)
	require.NoError(t, err)
	assert.Equal(t, []string{"v0.30.0", "v0.31.0", "v0.31.1"}, got)
}

func TestFilterTags_IncludePreAdmitsRCAndBeta(t *testing.T) {
	in := []string{"v0.30.0", "v1.0.0-rc.1", "recipes-abc1234", "v2.0.0-beta"}
	got, err := FilterTags(in, true /*includePre*/)
	require.NoError(t, err)
	assert.Equal(t, []string{"v0.30.0", "v1.0.0-rc.1", "v2.0.0-beta"}, got)
}

func TestFilterTags_EmptyInputIsEmpty(t *testing.T) {
	got, err := FilterTags(nil, false)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestFilterTags_PreservesInputOrder(t *testing.T) {
	// FilterTags must NOT sort — caller (go-selfupdate, the picker)
	// imposes its own order. We just gate which tags are allowed.
	in := []string{"v0.31.0", "v0.30.0", "v0.31.1"}
	got, err := FilterTags(in, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"v0.31.0", "v0.30.0", "v0.31.1"}, got)
}
