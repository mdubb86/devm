package main

import (
	"os/user"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildUninstallScript_ReapsIronProxyChildren(t *testing.T) {
	// The daemon's iron-proxy children survive its death by design
	// (setsid on spawn — see runner.go). Uninstall must SIGTERM them
	// itself; without this the e2e harness has to reap orphans, and
	// real users end up with iron-proxy processes sitting on
	// MAC_HOST:port bindings that leak across uninstall/reinstall.
	script := buildUninstallScript("/usr/local/bin/devm")

	require.True(t, strings.Contains(script, "launchctl bootout system/com.devm.service"),
		"script must SIGTERM the daemon via launchctl bootout")
	pkill := `pkill -TERM -f 'iron-proxy -config .*/iron-proxy/.*\.yaml'`
	require.True(t, strings.Contains(script, pkill),
		"script must pkill iron-proxy children with the daemon's own argv pattern; got:\n%s", script)

	// Ordering: bootout the daemon first, THEN pkill iron-proxies.
	// The other order can race — daemon-supervisor might respawn a
	// SIGTERM'd child before we've killed it.
	bootIdx := strings.Index(script, "launchctl bootout")
	pkillIdx := strings.Index(script, "pkill -TERM -f 'iron-proxy")
	require.Greater(t, pkillIdx, bootIdx,
		"pkill iron-proxy must come after launchctl bootout; got:\n%s", script)
}

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

