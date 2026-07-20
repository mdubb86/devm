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

	"github.com/mdubb86/devm/internal/identity"
)

func TestBuildUninstallScript_ReapsIronProxyChildren(t *testing.T) {
	// The daemon's iron-proxy children survive its death by design
	// (setsid on spawn — see runner.go). Uninstall must SIGTERM them
	// itself; without this the e2e harness has to reap orphans, and
	// real users end up with iron-proxy processes sitting on
	// MAC_HOST:port bindings that leak across uninstall/reinstall.
	script := buildUninstallScript(cfg, "/usr/local/bin/devm")

	require.True(t, strings.Contains(script, "launchctl bootout system/com.devm.service"),
		"script must SIGTERM the daemon via launchctl bootout")
	// Anchored on cfg.RuntimeDir()+"/iron-proxy/" (not a shared
	// "iron-proxy/*.yaml" glob): prod's pattern matches
	// .../devm/iron-proxy/, e2e's matches .../devm-e2e/iron-proxy/ —
	// disjoint, so `devm-e2e uninstall` can't reap the user's real
	// iron-proxy children.
	pkill := "pkill -TERM -f " + shellQuote(cfg.RuntimeDir()+"/iron-proxy/")
	require.True(t, strings.Contains(script, pkill),
		"script must pkill iron-proxy children anchored on this identity's runtime dir; got:\n%s", script)

	// Ordering: bootout the daemon first, THEN pkill iron-proxies.
	// The other order can race — daemon-supervisor might respawn a
	// SIGTERM'd child before we've killed it.
	bootIdx := strings.Index(script, "launchctl bootout")
	pkillIdx := strings.Index(script, "pkill -TERM -f")
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

// TestBuildInstallScript_NeedsGroupWithoutHelper pins the dedicated
// needsGroup gate: the group can need (re)creating even when the
// helper itself doesn't need reinstalling (e.g. the group was deleted
// externally).
func TestBuildInstallScript_NeedsGroupWithoutHelper(t *testing.T) {
	script := buildInstallScript(installInputs{
		DevmExe:    "/usr/local/bin/devm",
		NeedsGroup: true,
	})
	assert.Contains(t, script, "dscl . -create /Groups/_devm")
	assert.NotContains(t, script, "com.devm.helper", "helper block itself must stay skipped")
}

// TestBuildInstallScript_NeedsAliasesAssertsWholePool pins the
// aliases gate: when true, the script (re)asserts every alias in
// cfg's pool, not a diffed subset (ifconfig alias creation is
// idempotent, so re-asserting present aliases is a harmless no-op).
func TestBuildInstallScript_NeedsAliasesAssertsWholePool(t *testing.T) {
	script := buildInstallScript(installInputs{
		DevmExe:      "/usr/local/bin/devm",
		NeedsAliases: true,
	})
	// cfg is the package identity (Prod under `go test`) — derived from
	// it rather than hardcoded so this doesn't silently stop covering
	// the actual pool if Prod's bounds ever change.
	for n := cfg.PoolStart; n <= cfg.PoolEnd; n++ {
		assert.Contains(t, script, fmt.Sprintf("ifconfig lo0 alias 127.42.0.%d", n))
	}
}

func TestBuildInstallScript_SkipsAliasesWhenNotNeeded(t *testing.T) {
	script := buildInstallScript(installInputs{
		DevmExe: "/usr/local/bin/devm",
	})
	assert.NotContains(t, script, "ifconfig lo0 alias")
}

// TestHelperPlistContent_UsesResolvedProgramPathAndIdentity pins the
// no-system-copy plist design: ProgramArguments points directly at
// the resolved sibling helper binary, and Label/GroupName/log paths
// all derive from the package cfg identity.
func TestHelperPlistContent_UsesResolvedProgramPathAndIdentity(t *testing.T) {
	content := helperPlistContent("/usr/local/bin/devm-helper", "/Users/alice/Library/Logs")
	assert.Contains(t, content, "<string>com.devm.helper</string>")
	assert.Contains(t, content, "<string>/usr/local/bin/devm-helper</string>")
	assert.Contains(t, content, "<string>_devm</string>")
	assert.Contains(t, content, "/Users/alice/Library/Logs/com.devm.helper.out.log")
	assert.Contains(t, content, "/Users/alice/Library/Logs/com.devm.helper.err.log")
}

func TestBuildUninstallScript_RemovesAliases(t *testing.T) {
	script := buildUninstallScript(cfg, "/usr/local/bin/devm")
	assert.Contains(t, script, "launchctl bootout system/com.devm.helper")
	// Alias cleanup for all 20 addresses.
	for n := 1; n <= 20; n++ {
		assert.Contains(t, script, fmt.Sprintf("ifconfig lo0 -alias 127.42.0.%d", n))
	}
	assert.Contains(t, script, "rm -f /Library/LaunchDaemons/com.devm.helper.plist")
	// No system-path copy to clean up: the helper's LaunchDaemon plist
	// points directly at the sibling binary next to the devm CLI, so
	// there's nothing installed under /usr/local/libexec to remove.
	assert.NotContains(t, script, "/usr/local/libexec/devm-helper")
}

// TestBuildUninstallScript_DeletesBaseImageForE2E pins spec §8.3: an
// e2e uninstall also `tart delete`s its base image so e2e's
// base-lifecycle tests get a clean slate. Prod does not get this line
// — see TestBuildUninstallScript_KeepsBaseImageForProd — a user's base
// image is expensive to rebuild and shouldn't vanish on uninstall.
func TestBuildUninstallScript_DeletesBaseImageForE2E(t *testing.T) {
	script := buildUninstallScript(identity.E2E, "/usr/local/bin/devm-e2e")
	assert.Contains(t, script, "tart delete "+shellQuote(identity.E2E.BaseImageName()))
}

func TestBuildUninstallScript_KeepsBaseImageForProd(t *testing.T) {
	script := buildUninstallScript(identity.Prod, "/usr/local/bin/devm")
	assert.NotContains(t, script, "tart delete")
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

// TestLaunchdTargetIsIdentityAware and TestLaunchdPlistIsIdentityAware lock
// the invariant that launchdBootstrap/launchdBootout compose their
// launchctl arguments from the package-level cfg (identity.Config) rather
// than a hardcoded prod string. These exist so that a regression back to
// `const launchdTarget = "system/com.devm.service"` (etc.) has a test
// sitting right next to the helpers pinning the identity-driven values.
func TestLaunchdTargetIsIdentityAware(t *testing.T) {
	// package cfg is set from identity.Load(); in tests, that's Prod.
	want := "system/com.devm.service"
	got := cfg.LaunchdTargetDaemon()
	assert.Equal(t, want, got)
}

func TestLaunchdPlistIsIdentityAware(t *testing.T) {
	want := "/Library/LaunchDaemons/com.devm.service.plist"
	got := cfg.LaunchdPlistDaemon()
	assert.Equal(t, want, got)
}
