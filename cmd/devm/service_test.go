package main

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
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

func TestBuildInstallScript_IncludesHelper(t *testing.T) {
	script := buildInstallScript(installInputs{
		DevmExe:     "/usr/local/bin/devm",
		HelperExe:   "/usr/local/libexec/devm-helper",
		InstallUser: "alice",
		NeedsDaemon: true,
	})
	assert.Contains(t, script, "dscl . -create /Groups/_devm")
	assert.Contains(t, script, "/Library/LaunchDaemons/com.devm.helper.plist")
	assert.Contains(t, script, "launchctl bootstrap system /Library/LaunchDaemons/com.devm.helper.plist")
	assert.Contains(t, script, "/usr/local/libexec/devm-helper")

	// The append must be guarded against duplicate GroupMembership
	// entries across repeated install runs (dscl -append has no
	// dedup of its own).
	assert.Contains(t, script, "dscl . -read /Groups/_devm GroupMembership")
	assert.Contains(t, script, "grep -qw")
	assert.Contains(t, script, "|| dscl . -append /Groups/_devm GroupMembership")
}

func TestBuildInstallScript_SkipsHelperWhenExeEmpty(t *testing.T) {
	// Empty HelperExe means "not needed this run" (already
	// installed and daemon in sync) — the block must not appear at
	// all, not even the idempotent group-creation line.
	script := buildInstallScript(installInputs{
		DevmExe:     "/usr/local/bin/devm",
		NeedsDaemon: true,
	})
	assert.NotContains(t, script, "_devm")
	assert.NotContains(t, script, "com.devm.helper")
}

func TestBuildUninstallScript_RemovesAliases(t *testing.T) {
	script := buildUninstallScript("/usr/local/bin/devm")
	assert.Contains(t, script, "launchctl bootout system/com.devm.helper")
	// Alias cleanup for all 20 addresses.
	for n := 1; n <= 20; n++ {
		assert.Contains(t, script, fmt.Sprintf("ifconfig lo0 -alias 127.42.0.%d", n))
	}
	assert.Contains(t, script, "rm -f /Library/LaunchDaemons/com.devm.helper.plist")
	assert.Contains(t, script, "rm -f /usr/local/libexec/devm-helper")
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

func TestSSHConfigIncluded_DetectsPresence(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	sshDir := filepath.Join(dir, ".ssh")
	require.NoError(t, os.MkdirAll(sshDir, 0o700))

	// Case 1: file absent → not included.
	assert.False(t, sshConfigIncluded("/some/path/ssh_config"))

	// Case 2: file present, no matching Include.
	require.NoError(t, os.WriteFile(filepath.Join(sshDir, "config"),
		[]byte("Host github.com\n    User git\n"), 0o600))
	assert.False(t, sshConfigIncluded("/some/path/ssh_config"))

	// Case 3: file present, matching Include line.
	require.NoError(t, os.WriteFile(filepath.Join(sshDir, "config"),
		[]byte("Include \"/some/path/ssh_config\"\nHost github.com\n"), 0o600))
	assert.True(t, sshConfigIncluded("/some/path/ssh_config"))
}
