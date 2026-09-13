package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mdubb86/devm/internal/identity"
)

// launchctlBootstrap is a test seam; production uses the real launchctl.
var launchctlBootstrap = defaultLaunchctlBootstrap

func defaultLaunchctlBootstrap(uid int, plistPath string) error {
	return exec.Command("launchctl", "bootstrap", fmt.Sprintf("gui/%d", uid), plistPath).Run()
}

// launchctlBootout is a test seam; production uses the real launchctl.
var launchctlBootout = defaultLaunchctlBootout

func defaultLaunchctlBootout(uid int, label string) error {
	return exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", uid, label)).Run()
}

// menuAppBundleName returns "devm" or "devm-e2e" — matches the
// xcodebuild product name.
func menuAppBundleName(cfg identity.Config) string {
	return cfg.Name
}

// launchAgentLabel returns the LaunchAgent label string (also the
// menu-bar app's bundle id).
func launchAgentLabel(cfg identity.Config) string {
	return "com.mdubb86." + menuAppBundleName(cfg) + ".menuapp"
}

// launchAgentPlistPath returns ~/Library/LaunchAgents/<label>.plist.
func launchAgentPlistPath(cfg identity.Config) string {
	return filepath.Join(os.Getenv("HOME"), "Library", "LaunchAgents", launchAgentLabel(cfg)+".plist")
}

// renderLaunchAgentPlist returns the plist XML text for cfg's menu-bar
// app. RunAtLoad + KeepAlive so the app relaunches at login and after
// a crash, matching how the daemon's own LaunchDaemon behaves.
func renderLaunchAgentPlist(cfg identity.Config) string {
	label := launchAgentLabel(cfg)
	appName := menuAppBundleName(cfg)
	program := filepath.Join("/Applications", appName+".app", "Contents", "MacOS", appName)
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTD/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>Program</key><string>%s</string>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
</dict>
</plist>
`, label, program)
}

// installMenuAppAt is the test-friendly form of installMenuApp: srcAppPath
// and appsDir are explicit rather than resolved from the repo layout and
// /Applications. Idempotent — replaces any prior copy at
// <appsDir>/<name>.app and any prior LaunchAgent registration.
func installMenuAppAt(cfg identity.Config, srcAppPath, appsDir string) error {
	dstAppPath := filepath.Join(appsDir, menuAppBundleName(cfg)+".app")
	if err := os.RemoveAll(dstAppPath); err != nil {
		return fmt.Errorf("remove old .app: %w", err)
	}
	if err := copyAppBundle(srcAppPath, dstAppPath); err != nil {
		return fmt.Errorf("copy .app: %w", err)
	}
	return registerLaunchAgent(cfg)
}

// registerLaunchAgent writes the LaunchAgent plist for cfg's menu-bar app
// and bootstraps it via launchctl. Idempotent — replaces any prior
// registration. Shared by installMenuAppAt (fresh .app copy) and the
// Homebrew-cask fallback in installMenuAppOrRegisterAt, where the .app
// is already in place and only the LaunchAgent needs registering.
func registerLaunchAgent(cfg identity.Config) error {
	plistPath := launchAgentPlistPath(cfg)
	if err := os.MkdirAll(filepath.Dir(plistPath), 0755); err != nil {
		return fmt.Errorf("create LaunchAgents dir: %w", err)
	}
	if err := os.WriteFile(plistPath, []byte(renderLaunchAgentPlist(cfg)), 0644); err != nil {
		return fmt.Errorf("write LaunchAgent plist: %w", err)
	}

	// Best-effort bootout first: a prior install may already have this
	// label loaded, and bootstrap on an already-loaded label fails.
	_ = launchctlBootout(os.Getuid(), launchAgentLabel(cfg))
	if err := launchctlBootstrap(os.Getuid(), plistPath); err != nil {
		return fmt.Errorf("launchctl bootstrap: %w", err)
	}
	return nil
}

// installMenuAppOrRegisterAt is the test-friendly form of installMenuApp:
// srcAppPath and appsDir are explicit rather than resolved from the repo
// layout and /Applications.
//
// A source build at srcAppPath (`just mac-build[-e2e]`) is the normal
// case and copies fresh into appsDir. When srcAppPath is missing but
// appsDir already holds a <name>.app, that's the Homebrew-cask install
// path: the cask's `app "devm.app"` stanza already placed the bundle
// before `devm install` ever runs, so the copy step is skipped and only
// the LaunchAgent is registered against the pre-existing bundle. If
// neither is present, returns an actionable error naming the build
// recipe.
func installMenuAppOrRegisterAt(cfg identity.Config, srcAppPath, appsDir string) error {
	if _, err := os.Stat(srcAppPath); err != nil {
		installedAppPath := filepath.Join(appsDir, menuAppBundleName(cfg)+".app")
		if _, statErr := os.Stat(installedAppPath); statErr == nil {
			return registerLaunchAgent(cfg)
		}
		recipe := "just mac-build"
		if cfg == identity.E2E {
			recipe = "just mac-build-e2e"
		}
		return fmt.Errorf("menu-bar app not built at %s or installed at %s (run: %s): %w", srcAppPath, installedAppPath, recipe, err)
	}
	return installMenuAppAt(cfg, srcAppPath, appsDir)
}

// installMenuApp is the production entry point, called from
// runInstallFlow. Non-fatal by contract from the caller's side: `just
// mac-build[-e2e]` is a separate build step from the daemon build, and
// a caller who hasn't run it should still be able to install the
// daemon — runInstallFlow logs this error to stderr rather than
// failing the whole install.
func installMenuApp(cfg identity.Config, repoRoot string) error {
	srcApp := filepath.Join(repoRoot, "bin", menuAppBundleName(cfg)+".app")
	return installMenuAppOrRegisterAt(cfg, srcApp, "/Applications")
}

// uninstallMenuAppAt is the test-friendly form of uninstallMenuApp, with
// appsDir explicit rather than /Applications. Lenient — missing state at
// any step is not an error.
func uninstallMenuAppAt(cfg identity.Config, appsDir string) error {
	_ = launchctlBootout(os.Getuid(), launchAgentLabel(cfg))
	if err := os.Remove(launchAgentPlistPath(cfg)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove LaunchAgent plist: %w", err)
	}
	if err := os.RemoveAll(filepath.Join(appsDir, menuAppBundleName(cfg)+".app")); err != nil {
		return fmt.Errorf("remove .app: %w", err)
	}
	return nil
}

// uninstallMenuApp is the production entry point, called from
// uninstallCmd's RunE. Reverses installMenuApp; idempotent and lenient
// (safe to call even when the menu-bar app was never installed).
func uninstallMenuApp(cfg identity.Config) error {
	return uninstallMenuAppAt(cfg, "/Applications")
}

// copyAppBundle recursively copies the .app bundle at src to dst,
// preserving permissions and symlinks (app bundles are full of
// symlinks — Contents/MacOS, framework versions, etc.).
func copyAppBundle(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(path, src)
		target := filepath.Join(dst, rel)
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return fmt.Errorf("read symlink %s: %w", path, err)
			}
			return os.Symlink(link, target)
		}
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		return os.WriteFile(target, data, info.Mode())
	})
}
