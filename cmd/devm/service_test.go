package main

import (
	"os"
	"os/user"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveInstallUser_UsesSudoUserWhenPresent(t *testing.T) {
	t.Setenv("SUDO_USER", "alice")
	t.Setenv("USER", "root")
	name, home, err := resolveInstallUser(func(_ string) (*user.User, error) {
		return &user.User{Username: "alice", HomeDir: "/Users/alice"}, nil
	})
	require.NoError(t, err)
	assert.Equal(t, "alice", name)
	assert.Equal(t, "/Users/alice", home)
}

func TestResolveInstallUser_FallsBackToUser(t *testing.T) {
	t.Setenv("SUDO_USER", "")
	t.Setenv("USER", "bob")
	name, home, err := resolveInstallUser(func(_ string) (*user.User, error) {
		return &user.User{Username: "bob", HomeDir: "/Users/bob"}, nil
	})
	require.NoError(t, err)
	assert.Equal(t, "bob", name)
	assert.Equal(t, "/Users/bob", home)
}

func TestResolveInstallUser_RefusesRoot(t *testing.T) {
	t.Setenv("SUDO_USER", "")
	t.Setenv("USER", "root")
	_, _, err := resolveInstallUser(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot install as root")
}

func TestOldLaunchAgentCheck_NoOldPlist(t *testing.T) {
	tmp := t.TempDir()
	// No file exists at the expected location.
	err := checkOldLaunchAgentPlist(filepath.Join(tmp, "com.devm.service.plist"))
	assert.NoError(t, err)
}

func TestOldLaunchAgentCheck_PlistPresent(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "com.devm.service.plist")
	require.NoError(t, os.WriteFile(path, []byte("<?xml ?>"), 0644))
	err := checkOldLaunchAgentPlist(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "previous-version devm install")
	assert.Contains(t, err.Error(), "launchctl bootout gui/")
}
