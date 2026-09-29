package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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

		// Extract the release tarball's devm.app bundle to a temp dir
		// and hand its path to `devm install` via DEVM_INSTALL_APP_SRC.
		// go-selfupdate above only swaps the CLI binary; without this
		// step the menu-bar app never gets updated on `devm upgrade`.
		//
		// Not best-effort: end users don't have a repo checkout to
		// fall back on. A fetch failure fails the whole upgrade with
		// an actionable recovery path (re-run upgrade, or manually
		// download the release). The CLI is already swapped by this
		// point — the user gets the new CLI but MUST re-run to also
		// get the .app.
		appSrc, cleanup, err := fetchAppFromRelease(ctx, rel.AssetURL)
		if err != nil {
			return fmt.Errorf("fetch menu-bar app from %s: %w\n\nThe CLI is updated but the menu-bar app was not. Re-run `devm upgrade` to retry, or download %s manually and extract devm.app into /Applications.", rel.AssetURL, err, rel.AssetURL)
		}
		defer cleanup()

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
		installCmd.Env = os.Environ()
		if appSrc != "" {
			installCmd.Env = append(installCmd.Env, "DEVM_INSTALL_APP_SRC="+appSrc)
		}
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

// fetchAppFromRelease downloads the release tarball at assetURL,
// extracts just its devm.app/ subtree to a temp dir, and returns the
// path to the extracted .app plus a cleanup function to remove the
// temp dir. Called by `devm upgrade` to feed the new .app bundle into
// the re-exec'd `devm install` via DEVM_INSTALL_APP_SRC.
//
// The tarball also contains the CLI binary and image assets; those
// are ignored — go-selfupdate has already swapped the CLI, and image
// assets are already inside the CLI's own bundle. Only devm.app/**
// is extracted.
//
// Errors are surfaced to the caller: the upgrade fails so the user
// runs into the recovery path rather than getting a half-upgraded
// install with a stale menu-bar app.
func fetchAppFromRelease(ctx context.Context, assetURL string) (string, func(), error) {
	tmpDir, err := os.MkdirTemp("", "devm-upgrade-app-")
	if err != nil {
		return "", func() {}, fmt.Errorf("mkdir temp: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
	if err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		cleanup()
		return "", func() {}, fmt.Errorf("download %s: HTTP %d", assetURL, resp.StatusCode)
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	extracted := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			cleanup()
			return "", func() {}, fmt.Errorf("tar next: %w", err)
		}
		if !strings.HasPrefix(hdr.Name, "devm.app/") {
			continue
		}
		outPath := filepath.Join(tmpDir, hdr.Name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(outPath, os.FileMode(hdr.Mode)&0o777|0o700); err != nil {
				cleanup()
				return "", func() {}, fmt.Errorf("mkdir %s: %w", outPath, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
				cleanup()
				return "", func() {}, fmt.Errorf("mkdir parent %s: %w", outPath, err)
			}
			f, err := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				cleanup()
				return "", func() {}, fmt.Errorf("open %s: %w", outPath, err)
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				cleanup()
				return "", func() {}, fmt.Errorf("write %s: %w", outPath, err)
			}
			if err := f.Close(); err != nil {
				cleanup()
				return "", func() {}, fmt.Errorf("close %s: %w", outPath, err)
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
				cleanup()
				return "", func() {}, fmt.Errorf("mkdir parent %s: %w", outPath, err)
			}
			if err := os.Symlink(hdr.Linkname, outPath); err != nil {
				cleanup()
				return "", func() {}, fmt.Errorf("symlink %s: %w", outPath, err)
			}
		}
		extracted = true
	}
	if !extracted {
		cleanup()
		return "", func() {}, fmt.Errorf("release tarball at %s contained no devm.app/ entries", assetURL)
	}
	return filepath.Join(tmpDir, "devm.app"), cleanup, nil
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
