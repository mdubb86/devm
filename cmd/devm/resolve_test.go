package main

import (
	"testing"

	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveAbsolutePath_InsideWorkspace(t *testing.T) {
	registry := []serviceapi.WorkspaceEntry{{
		Project: "sewtrue", GuestPath: "/Users/me/projects/sewtrue",
		StoragePath: "/Users/me/Library/Application Support/devm/volumes/sewtrue/sewtrue",
	}}
	got, err := resolvePath("/Users/me/projects/sewtrue/tests/output/foo.png", "", registry)
	require.NoError(t, err)
	assert.Equal(t, "/Users/me/Library/Application Support/devm/volumes/sewtrue/sewtrue/tests/output/foo.png", got)
}

func TestResolveRelativePath_CwdInsideProject(t *testing.T) {
	registry := []serviceapi.WorkspaceEntry{{
		Project: "sewtrue", GuestPath: "/Users/me/projects/sewtrue",
		StoragePath: "/vol/sewtrue",
	}}
	got, err := resolvePath("tests/output/foo.png", "/Users/me/projects/sewtrue", registry)
	require.NoError(t, err)
	assert.Equal(t, "/vol/sewtrue/tests/output/foo.png", got)
}

func TestResolvePath_OutsideAny_Errors(t *testing.T) {
	registry := []serviceapi.WorkspaceEntry{{
		Project: "sewtrue", GuestPath: "/Users/me/projects/sewtrue",
		StoragePath: "/vol/sewtrue",
	}}
	_, err := resolvePath("/tmp/foo", "/tmp", registry)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not inside any known devm workspace")
}

func TestResolvePath_ExactWorkspaceRoot(t *testing.T) {
	registry := []serviceapi.WorkspaceEntry{{
		Project: "sewtrue", GuestPath: "/Users/me/projects/sewtrue",
		StoragePath: "/vol/sewtrue",
	}}
	got, err := resolvePath("/Users/me/projects/sewtrue", "", registry)
	require.NoError(t, err)
	assert.Equal(t, "/vol/sewtrue", got)
}
