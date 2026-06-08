package recipes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultReleasesURL = "https://api.github.com/repos/mdubb86/devm/releases"

	lazyTimeout     = 5 * time.Second
	explicitTimeout = 30 * time.Second
	lazyMaxAge      = 24 * time.Hour
)

// Syncer manages the cached recipes.db at <cacheDir>/recipes.db and
// the freshness timestamp at <cacheDir>/recipes.lastcheck.
type Syncer struct {
	cacheDir    string
	releasesURL string
	client      *http.Client
}

// NewSyncer constructs a Syncer. cacheDir is created if missing on
// the first Sync call.
func NewSyncer(cacheDir, releasesURL string) *Syncer {
	if releasesURL == "" {
		releasesURL = defaultReleasesURL
	}
	return &Syncer{
		cacheDir:    cacheDir,
		releasesURL: releasesURL,
		client:      &http.Client{},
	}
}

// SyncResult describes what Sync did.
type SyncResult struct {
	Version    string // tag name from the picked release
	Downloaded bool   // true if a new DB was fetched
}

// Sync runs the cache refresh. lazy=true means:
//   - Skip the remote check if the lastcheck timestamp is < 24h old.
//   - Silently swallow network errors (returns SyncResult{Downloaded:false}, nil).
//
// lazy=false ("explicit", e.g., `devm recipes sync`):
//   - Always hit the remote.
//   - Propagate network/HTTP errors.
func (s *Syncer) Sync(ctx context.Context, lazy bool) (SyncResult, error) {
	if err := os.MkdirAll(s.cacheDir, 0o755); err != nil {
		return SyncResult{}, fmt.Errorf("recipes sync: mkdir %s: %w", s.cacheDir, err)
	}

	if lazy {
		fresh, err := s.cacheFresh()
		if err == nil && fresh {
			return SyncResult{Downloaded: false}, nil
		}
	}

	timeout := lazyTimeout
	if !lazy {
		timeout = explicitTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	latestTag, assetURL, err := s.fetchLatestReleaseInfo(ctx)
	if err != nil {
		if lazy {
			return SyncResult{Downloaded: false}, nil
		}
		return SyncResult{}, err
	}

	// If cache version already equals latestTag, just touch the timestamp.
	if existing := s.cachedVersion(); existing == latestTag {
		_ = s.touchLastcheck()
		return SyncResult{Version: latestTag, Downloaded: false}, nil
	}

	if err := s.downloadAndReplace(ctx, assetURL); err != nil {
		if lazy {
			return SyncResult{Downloaded: false}, nil
		}
		return SyncResult{}, err
	}
	_ = s.touchLastcheck()
	return SyncResult{Version: latestTag, Downloaded: true}, nil
}

func (s *Syncer) cacheFresh() (bool, error) {
	raw, err := os.ReadFile(filepath.Join(s.cacheDir, "recipes.lastcheck"))
	if err != nil {
		return false, err
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(raw)))
	if err != nil {
		return false, err
	}
	return time.Since(t) < lazyMaxAge, nil
}

func (s *Syncer) touchLastcheck() error {
	return os.WriteFile(filepath.Join(s.cacheDir, "recipes.lastcheck"),
		[]byte(time.Now().Format(time.RFC3339)), 0o644)
}

func (s *Syncer) cachedVersion() string {
	q, err := Open(filepath.Join(s.cacheDir, "recipes.db"))
	if err != nil {
		return ""
	}
	defer q.Close()
	v, err := q.Version()
	if err != nil {
		return ""
	}
	return v
}

type ghRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

func (s *Syncer) fetchLatestReleaseInfo(ctx context.Context) (tag, assetURL string, err error) {
	req, err := http.NewRequestWithContext(ctx, "GET", s.releasesURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := s.client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", "", fmt.Errorf("recipes sync: releases endpoint returned %d", resp.StatusCode)
	}
	var releases []ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return "", "", err
	}
	for _, r := range releases {
		if !strings.HasPrefix(r.TagName, "recipes-") {
			continue
		}
		for _, a := range r.Assets {
			if a.Name == "recipes.db" {
				return r.TagName, a.URL, nil
			}
		}
	}
	return "", "", errors.New("recipes sync: no recipes-* release with recipes.db asset found")
}

func (s *Syncer) downloadAndReplace(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("recipes sync: download returned %d", resp.StatusCode)
	}

	tmpPath := filepath.Join(s.cacheDir, "recipes.db.tmp")
	tmp, err := os.Create(tmpPath)
	if err != nil {
		return err
	}
	// 50 MB hard ceiling. A misbehaving releases endpoint shouldn't be
	// able to exhaust disk through this code path. LimitReader+1 lets
	// us detect oversize bodies after the copy.
	const maxRecipesDB = 50 * 1024 * 1024
	n, err := io.Copy(tmp, io.LimitReader(resp.Body, maxRecipesDB+1))
	if err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	tmp.Close()
	if n > maxRecipesDB {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("recipes sync: downloaded db exceeds %d-byte ceiling", maxRecipesDB)
	}

	// Validate by opening + version-checking.
	q, err := Open(tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("recipes sync: downloaded db invalid: %w", err)
	}
	q.Close()

	if err := os.Rename(tmpPath, filepath.Join(s.cacheDir, "recipes.db")); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

// CacheDir returns the on-disk cache directory location, honoring the
// DEVM_RECIPES_CACHE_DIR env override if set. Used by the cobra command
// to construct a Syncer pointing at the right place.
func CacheDir() string {
	if v := os.Getenv("DEVM_RECIPES_CACHE_DIR"); v != "" {
		return v
	}
	if v := os.Getenv("XDG_CACHE_HOME"); v != "" {
		return filepath.Join(v, "devm")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".cache", "devm")
	}
	return ".cache/devm"
}

// ReleasesURL returns the configured releases endpoint, honoring the
// DEVM_RECIPES_URL env override for tests.
func ReleasesURL() string {
	if v := os.Getenv("DEVM_RECIPES_URL"); v != "" {
		return v
	}
	return defaultReleasesURL
}
