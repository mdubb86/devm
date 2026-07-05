package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	selfupdate "github.com/creativeprojects/go-selfupdate"
	"github.com/mdubb86/devm/internal/image"
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

		// Rebuild base Tart image if the new binary ships updated
		// image definition. Best-effort — binary IS updated either way;
		// run `devm install` to retry on failure. The provisioning
		// script is embedded (//go:embed), so we don't need an on-disk
		// image/ directory anymore.
		needs, _, _ := image.NeedsBuild("")
		if needs {
			fmt.Println("Rebuilding devm-base after binary upgrade...")
			if err := image.BuildBaseImage(cmd.Context(), "", os.Stdout); err != nil {
				fmt.Fprintf(os.Stderr, "note: rebuild failed (%v). Run `devm install` to retry.\n", err)
			}
		}

		// If the daemon is running, restart it to pick up the new
		// binary. Best-effort — binary IS replaced either way.
		if err := restartAndWait("upgraded to " + rel.Version()); err != nil {
			fmt.Fprintf(os.Stderr, "warning: %v\n", err)
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
