package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	selfupdate "github.com/creativeprojects/go-selfupdate"
	"github.com/mdubb86/devm/internal/recipes"
	"github.com/mdubb86/devm/internal/release"
	"github.com/spf13/cobra"
)

var upgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Upgrade devm to the latest release",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true

		execPath, err := os.Executable()
		if err != nil {
			return fmt.Errorf("resolving executable path: %w", err)
		}
		execPath, err = filepath.EvalSymlinks(execPath)
		if err != nil {
			return fmt.Errorf("resolving symlinks: %w", err)
		}

		ctx := cmd.Context()

		if release.Classify(ctx, execPath, release.DefaultBrewLister()) == release.SourceBrew {
			fmt.Fprintf(os.Stderr, "devm is installed via Homebrew:\n  %s\n\nTo upgrade, run:\n  brew upgrade mdubb86/tap/devm\n\n(Refusing to self-update — would create a brew/binary version mismatch.)\n", execPath)
			os.Exit(1)
		}

		updater, err := newUpdater()
		if err != nil {
			return fmt.Errorf("creating updater: %w", err)
		}

		repo := selfupdate.ParseSlug("mdubb86/devm")
		rel, found, err := updater.DetectLatest(ctx, repo)
		if err != nil {
			return fmt.Errorf("detecting latest release: %w", err)
		}
		if !found {
			fmt.Println("no release found")
			return nil
		}

		if rel.Equal(Version) || rel.LessThan(Version) {
			fmt.Printf("already at latest version %s\n", Version)
			return nil
		}

		if err := updater.UpdateTo(ctx, rel, execPath); err != nil {
			return fmt.Errorf("updating binary: %w", err)
		}

		fmt.Printf("upgraded to %s\n", rel.Version())

		// Refresh recipes too — they're on their own release cadence
		// (recipes-<sha> tags fire on every push to recipes/**), so an
		// upgraded devm otherwise sees whatever was current the last
		// time the user ran `devm recipes list` (up to 24h stale via
		// lazy sync). Explicit sync bypasses the 24h rate limit — the
		// user's `devm upgrade` is exactly the "give me freshest"
		// signal. Best-effort: a network failure here shouldn't fail
		// the upgrade, since the binary swap already succeeded.
		syncer := recipes.NewSyncer(recipes.CacheDir(), recipes.ReleasesURL())
		if _, err := syncer.Sync(ctx, false); err != nil {
			fmt.Fprintf(os.Stderr, "warning: recipes sync failed (%v). Run `devm recipes sync` to retry.\n", err)
		}

		// Run the install flow via a re-exec of the newly-written
		// binary. We can't call runInstallFlow directly from THIS
		// process — this process was compiled from the OLD version, so
		// its Fingerprint constant matches the still-running old
		// daemon. daemonInSyncWithCLI would return true, install would
		// early-out, and the daemon would never restart onto the new
		// bytes. The re-exec runs the freshly-written binary whose
		// compiled Fingerprint doesn't match the running daemon, so
		// the sync check correctly detects drift and does the full
		// plist swap + restart. `devm install` is in
		// skipDriftCheckPaths so PersistentPreRun won't block it.
		installCmd := exec.CommandContext(ctx, execPath, "install")
		installCmd.Stdout = os.Stdout
		installCmd.Stderr = os.Stderr
		installCmd.Stdin = os.Stdin
		if err := installCmd.Run(); err != nil {
			return fmt.Errorf("post-upgrade install: %w", err)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(upgradeCmd)
}

// newUpdater constructs a go-selfupdate Updater configured for devm stable
// releases on GitHub. The Filters field ensures only devm_v*_darwin_*.tar.gz
// assets are considered, excluding pre-releases and recipes-* tags.
func newUpdater() (*selfupdate.Updater, error) {
	source, err := selfupdate.NewGitHubSource(selfupdate.GitHubConfig{})
	if err != nil {
		return nil, err
	}
	return selfupdate.NewUpdater(selfupdate.Config{
		Source: source,
		Filters: []string{
			`^devm_v\d+(\.\d+){0,2}_darwin_(arm64|amd64)\.tar\.gz$`,
		},
	})
}

// fetchLatestForCheck returns the latest stable release tag for devm, or an
// empty string on any error. It is intentionally silent — --check is
// informational only.
func fetchLatestForCheck(ctx context.Context) string {
	updater, err := newUpdater()
	if err != nil {
		return ""
	}
	repo := selfupdate.ParseSlug("mdubb86/devm")
	rel, found, err := updater.DetectLatest(ctx, repo)
	if err != nil || !found {
		return ""
	}
	return rel.Version()
}
