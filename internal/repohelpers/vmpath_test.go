package repohelpers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTranslateGuestPath_ExactMatch(t *testing.T) {
	registry := []WorkspacePathEntry{
		{GuestPath: "/Users/x/proj", StoragePath: "/Library/vol/proj"},
	}
	got, err := TranslateGuestPath("/Users/x/proj", registry)
	require.NoError(t, err)
	assert.Equal(t, "/Library/vol/proj", got)
}

func TestTranslateGuestPath_Subpath(t *testing.T) {
	registry := []WorkspacePathEntry{
		{GuestPath: "/Users/x/proj", StoragePath: "/Library/vol/proj"},
	}
	got, err := TranslateGuestPath("/Users/x/proj/src/foo.png", registry)
	require.NoError(t, err)
	assert.Equal(t, "/Library/vol/proj/src/foo.png", got)
}

func TestTranslateGuestPath_MultipleWorkspaces(t *testing.T) {
	registry := []WorkspacePathEntry{
		{GuestPath: "/Users/x/a", StoragePath: "/Library/vol/a"},
		{GuestPath: "/Users/x/b", StoragePath: "/Library/vol/b"},
	}
	got, err := TranslateGuestPath("/Users/x/b/foo", registry)
	require.NoError(t, err)
	assert.Equal(t, "/Library/vol/b/foo", got)
}

func TestTranslateGuestPath_OutsideAnyWorkspace(t *testing.T) {
	registry := []WorkspacePathEntry{
		{GuestPath: "/Users/x/proj", StoragePath: "/Library/vol/proj"},
	}
	_, err := TranslateGuestPath("/Users/x/somewhere-else/foo", registry)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not inside any known devm workspace")
}

func TestTranslateGuestPath_NewLayout(t *testing.T) {
	// Task 16's mirror layout: guest paths live under /home/devm/<label>
	// and translate to <runtimeDir>/<projectID>/<label>/ on the Mac side.
	registry := []WorkspacePathEntry{
		{GuestPath: "/home/devm/repo", StoragePath: "/base/proj/repo"},
	}
	got, err := TranslateGuestPath("/home/devm/repo/foo/bar", registry)
	require.NoError(t, err)
	assert.Equal(t, "/base/proj/repo/foo/bar", got)
}

func TestTranslateGuestPath_PrefixCollisionAvoidance(t *testing.T) {
	// A guest path that starts with a workspace's GuestPath as a string
	// but is not actually inside it (e.g., "/Users/x/projX" vs "/Users/x/proj")
	// must not match.
	registry := []WorkspacePathEntry{
		{GuestPath: "/Users/x/proj", StoragePath: "/Library/vol/proj"},
	}
	_, err := TranslateGuestPath("/Users/x/projX/foo", registry)
	require.Error(t, err)
}
