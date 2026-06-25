package release

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/mdubb86/devm/internal/status"
)

const (
	// nudgeMaxAge bounds how often we hit GitHub. A week is the
	// sweet spot — frequent enough that users see new releases
	// within a few uses, rare enough that GitHub rate limits and
	// shell-startup overhead stay negligible.
	nudgeMaxAge        = 7 * 24 * time.Hour
	nudgeCheckFileName = "version-check"
	nudgeFetchTimeout  = 3 * time.Second
)

type nudgeCache struct {
	CheckedAt int64  `json:"checked_at"`
	LatestTag string `json:"latest_tag"`
}

// FetchLatestFunc returns the latest stable release tag string
// (without "v" prefix, matching ldflag-injected Version) or empty
// on any error. Callers inject this so internal/release stays
// independent of go-selfupdate.
type FetchLatestFunc func(context.Context) string

// MaybeNudge prints one line to w when a newer release exists:
//
//	devm v<latest> available — run `devm upgrade`
//
// Suppressed when any of these hold:
//   - $DEVM_NO_UPDATE_CHECK=1   (user opted out)
//   - $CI=true                  (CI env — no human to nudge)
//   - currentVersion is "dev"   (dev build, ldflags not injected)
//   - the binary is brew-managed (brew upgrade is the right path,
//     not devm upgrade — same logic as `devm upgrade` itself)
//
// Cache: ~/.cache/devm/version-check stores the last (checked_at,
// latest_tag). Behavior:
//   - Cache fresh (<7d) → print nudge immediately if latest differs
//     from currentVersion. No network hit, no spinner.
//   - Cache stale or missing → show a single-line spinner
//     "checking for devm updates", fetch the latest tag with a 3s
//     timeout, finalize spinner as ✓ (or skip on error), then print
//     the nudge if newer. Subsequent calls within 7d hit the cache.
//
// The spinner is rendered to w (typically os.Stderr) via
// internal/status. Non-TTY callers (CI, piped stderr) get a plain
// transcript line instead of an animated spinner.
func MaybeNudge(ctx context.Context, w io.Writer, currentVersion string, fetchLatest FetchLatestFunc, brewLister BrewLister) {
	if os.Getenv("DEVM_NO_UPDATE_CHECK") == "1" {
		return
	}
	if os.Getenv("CI") == "true" {
		return
	}
	if currentVersion == "" || currentVersion == "dev" {
		return
	}
	if execPath := resolvedExec(); execPath != "" {
		if Classify(ctx, execPath, brewLister) == SourceBrew {
			return
		}
	}

	cachePath := cachePathForNudge()
	cache, _ := readNudgeCache(cachePath)

	cacheFresh := cache.LatestTag != "" &&
		time.Since(time.Unix(cache.CheckedAt, 0)) < nudgeMaxAge

	if cacheFresh {
		if IsNewer(cache.LatestTag, currentVersion) {
			fmt.Fprintf(w, "devm v%s available — run `devm upgrade`\n", cache.LatestTag)
		}
		return
	}

	// Cache stale or missing — fetch synchronously with a spinner.
	// 3s timeout keeps the worst case bounded.
	if fetchLatest == nil {
		return
	}

	reporter := status.New(w)
	reporter.Start("checking for devm updates")
	fetchCtx, cancel := context.WithTimeout(ctx, nudgeFetchTimeout)
	defer cancel()
	latest := fetchLatest(fetchCtx)

	reporter.Stop()
	reporter.Clear()

	if latest == "" {
		return
	}
	_ = writeNudgeCache(cachePath, nudgeCache{
		CheckedAt: time.Now().Unix(),
		LatestTag: latest,
	})
	if IsNewer(latest, currentVersion) {
		fmt.Fprintf(w, "devm v%s available — run `devm upgrade`\n", latest)
	}
}

func cachePathForNudge() string {
	if v := os.Getenv("DEVM_NUDGE_CACHE_DIR"); v != "" {
		return filepath.Join(v, nudgeCheckFileName)
	}
	if v := os.Getenv("XDG_CACHE_HOME"); v != "" {
		return filepath.Join(v, "devm", nudgeCheckFileName)
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".cache", "devm", nudgeCheckFileName)
	}
	return filepath.Join(".cache", "devm", nudgeCheckFileName)
}

func readNudgeCache(path string) (nudgeCache, error) {
	var c nudgeCache
	data, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return nudgeCache{}, err
	}
	return c, nil
}

func writeNudgeCache(path string, c nudgeCache) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func resolvedExec() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	if abs, err := filepath.EvalSymlinks(p); err == nil {
		return abs
	}
	return p
}
