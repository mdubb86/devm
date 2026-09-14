package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLaunchAgentPlistPath_MatchesCfgName(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	got := launchAgentPlistPath(identity.Prod)
	assert.Equal(t, filepath.Join(tmp, "Library", "LaunchAgents", "com.mdubb86.devm.menuapp.plist"), got)
	got = launchAgentPlistPath(identity.E2E)
	assert.Equal(t, filepath.Join(tmp, "Library", "LaunchAgents", "com.mdubb86.devm-e2e.menuapp.plist"), got)
}

func TestRenderLaunchAgentPlist_ContainsLabelAndProgram(t *testing.T) {
	plist := renderLaunchAgentPlist(identity.Prod)
	assert.Contains(t, plist, "<key>Label</key>")
	assert.Contains(t, plist, "<string>com.mdubb86.devm.menuapp</string>")
	assert.Contains(t, plist, "<key>Program</key>")
	assert.Contains(t, plist, "<string>/Applications/devm.app/Contents/MacOS/devm</string>")
	assert.Contains(t, plist, "<key>RunAtLoad</key>")
	assert.Contains(t, plist, "<key>KeepAlive</key>")
}

func TestInstallMenuApp_CopiesAppAndWritesPlist(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	// Simulate a built .app at ./bin/devm.app relative to a fake repo root.
	repoBin := filepath.Join(tmp, "src", "bin")
	require.NoError(t, os.MkdirAll(filepath.Join(repoBin, "devm.app"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(repoBin, "devm.app", "test-marker"), []byte("hi"), 0644))

	// Redirect /Applications/ to a tempdir for the test.
	appsDir := filepath.Join(tmp, "Applications")
	require.NoError(t, os.MkdirAll(appsDir, 0755))

	// Skip actual launchctl invocations in tests via injected func.
	var bootstrapCalls int
	launchctlBootstrap = func(uid int, plistPath string) error {
		bootstrapCalls++
		return nil
	}
	defer func() { launchctlBootstrap = defaultLaunchctlBootstrap }()

	err := installMenuAppAt(identity.Prod, filepath.Join(repoBin, "devm.app"), appsDir)
	require.NoError(t, err)

	// .app was copied.
	_, err = os.Stat(filepath.Join(appsDir, "devm.app", "test-marker"))
	assert.NoError(t, err)

	// LaunchAgent plist exists with right contents.
	plistBytes, err := os.ReadFile(launchAgentPlistPath(identity.Prod))
	require.NoError(t, err)
	assert.Contains(t, string(plistBytes), "com.mdubb86.devm.menuapp")

	// launchctl bootstrap was called.
	assert.Equal(t, 1, bootstrapCalls)
}

func TestInstallMenuApp_MissingAppReturnsError(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	repoRoot := filepath.Join(tmp, "src")
	require.NoError(t, os.MkdirAll(repoRoot, 0755))
	// appsDir exists but has no pre-existing <name>.app, so there is no
	// fallback and this must error.
	appsDir := filepath.Join(tmp, "Applications")
	require.NoError(t, os.MkdirAll(appsDir, 0755))

	srcApp := filepath.Join(repoRoot, "bin", "devm.app")
	err := installMenuAppOrRegisterAt(identity.Prod, srcApp, appsDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "just mac-build")
	assert.NotContains(t, err.Error(), "just mac-build-e2e")
}

func TestInstallMenuApp_MissingAppRecipeIsE2EAware(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	repoRoot := filepath.Join(tmp, "src")
	require.NoError(t, os.MkdirAll(repoRoot, 0755))
	appsDir := filepath.Join(tmp, "Applications")
	require.NoError(t, os.MkdirAll(appsDir, 0755))

	srcApp := filepath.Join(repoRoot, "bin", "devm-e2e.app")
	err := installMenuAppOrRegisterAt(identity.E2E, srcApp, appsDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "just mac-build-e2e")
}

func TestInstallMenuApp_FallsBackToPreExistingAppInApplications(t *testing.T) {
	// The Homebrew-cask path: the cask's `app "devm.app"` stanza has
	// already placed the bundle in appsDir, but bin/devm.app (the
	// source-build output) doesn't exist because this is a brew install,
	// not a `just mac-build` one. installMenuAppOrRegisterAt must skip
	// the copy and just register the LaunchAgent against the app that's
	// already there.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	repoRoot := filepath.Join(tmp, "src")
	require.NoError(t, os.MkdirAll(repoRoot, 0755))

	appsDir := filepath.Join(tmp, "Applications")
	require.NoError(t, os.MkdirAll(filepath.Join(appsDir, "devm.app"), 0755))

	var bootstrapCalls int
	launchctlBootstrap = func(uid int, plistPath string) error {
		bootstrapCalls++
		return nil
	}
	defer func() { launchctlBootstrap = defaultLaunchctlBootstrap }()

	srcApp := filepath.Join(repoRoot, "bin", "devm.app") // not built
	err := installMenuAppOrRegisterAt(identity.Prod, srcApp, appsDir)
	require.NoError(t, err)

	plistBytes, err := os.ReadFile(launchAgentPlistPath(identity.Prod))
	require.NoError(t, err)
	assert.Contains(t, string(plistBytes), "com.mdubb86.devm.menuapp")

	assert.Equal(t, 1, bootstrapCalls)
}

func TestUninstallMenuApp_RemovesAppAndPlist(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	repoBin := filepath.Join(tmp, "src", "bin")
	require.NoError(t, os.MkdirAll(filepath.Join(repoBin, "devm.app"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(repoBin, "devm.app", "test-marker"), []byte("hi"), 0644))

	appsDir := filepath.Join(tmp, "Applications")
	require.NoError(t, os.MkdirAll(appsDir, 0755))

	launchctlBootstrap = func(uid int, plistPath string) error { return nil }
	defer func() { launchctlBootstrap = defaultLaunchctlBootstrap }()

	require.NoError(t, installMenuAppAt(identity.Prod, filepath.Join(repoBin, "devm.app"), appsDir))

	var bootoutCalls int
	var bootoutLabel string
	launchctlBootout = func(uid int, label string) error {
		bootoutCalls++
		bootoutLabel = label
		return nil
	}
	defer func() { launchctlBootout = defaultLaunchctlBootout }()

	require.NoError(t, uninstallMenuAppAt(identity.Prod, appsDir))

	assert.Equal(t, 1, bootoutCalls)
	assert.Equal(t, "com.mdubb86.devm.menuapp", bootoutLabel)

	_, err := os.Stat(filepath.Join(appsDir, "devm.app"))
	assert.True(t, os.IsNotExist(err))

	_, err = os.Stat(launchAgentPlistPath(identity.Prod))
	assert.True(t, os.IsNotExist(err))
}

func TestUninstallMenuApp_IdempotentWhenNothingInstalled(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	launchctlBootout = func(uid int, label string) error { return nil }
	defer func() { launchctlBootout = defaultLaunchctlBootout }()

	// Nothing was ever installed; must not error.
	assert.NoError(t, uninstallMenuApp(identity.Prod))
}
