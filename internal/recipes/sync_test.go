package recipes

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildTinyDB writes a minimal valid recipes DB to dstPath. Returns
// the version string stamped in meta.
func buildTinyDB(t *testing.T, dstPath, version string) {
	t.Helper()
	_ = os.Remove(dstPath)
	db, err := sql.Open("sqlite", dstPath)
	require.NoError(t, err)
	defer db.Close()
	ctx := context.Background()
	for _, s := range []string{
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE TABLE recipes (
			name TEXT PRIMARY KEY, category TEXT NOT NULL,
			display_name TEXT NOT NULL, description TEXT NOT NULL,
			keywords TEXT NOT NULL, content TEXT NOT NULL,
			since TEXT, updated_at INTEGER NOT NULL
		)`,
		`CREATE VIRTUAL TABLE recipes_fts USING fts5(name, display_name, description, keywords, content)`,
	} {
		_, err := db.ExecContext(ctx, s)
		require.NoError(t, err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO meta VALUES ('version', ?)`, version)
	require.NoError(t, err)
}

// fakeReleasesServer returns a tiny httptest server that mimics the
// two endpoints sync.go hits: GET releases list (JSON), and GET the
// recipes.db artifact (binary).
func fakeReleasesServer(t *testing.T, version, dbPath string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases":
			// Return one release whose name is `version` and whose
			// recipes.db asset URL points back at this same server.
			w.Header().Set("Content-Type", "application/json")
			body := `[{"tag_name":"` + version + `","assets":[{"name":"recipes.db","browser_download_url":"` +
				"http://" + r.Host + "/recipes.db" + `"}]}]`
			_, _ = w.Write([]byte(body))
		case "/recipes.db":
			http.ServeFile(w, r, dbPath)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestSync_FreshDownloadsDB(t *testing.T) {
	cacheDir := t.TempDir()
	srcDB := filepath.Join(t.TempDir(), "remote-recipes.db")
	buildTinyDB(t, srcDB, "recipes-v1.0.0")

	srv := fakeReleasesServer(t, "recipes-v1.0.0", srcDB)
	defer srv.Close()

	s := NewSyncer(cacheDir, srv.URL+"/releases")
	res, err := s.Sync(context.Background(), false /*lazy*/)
	require.NoError(t, err)
	assert.Equal(t, "recipes-v1.0.0", res.Version)
	assert.True(t, res.Downloaded)

	_, err = os.Stat(filepath.Join(cacheDir, "recipes.db"))
	require.NoError(t, err)
}

func TestSync_LazySkipsWhenCacheFresh(t *testing.T) {
	cacheDir := t.TempDir()
	srcDB := filepath.Join(t.TempDir(), "remote-recipes.db")
	buildTinyDB(t, srcDB, "recipes-v1.0.0")

	// Pre-populate cache + lastcheck timestamp ~ now.
	buildTinyDB(t, filepath.Join(cacheDir, "recipes.db"), "recipes-v1.0.0")
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "recipes.lastcheck"),
		[]byte(time.Now().Format(time.RFC3339)), 0o644))

	// No server — if sync tries to hit the network it'll fail.
	s := NewSyncer(cacheDir, "http://127.0.0.1:1/releases")
	res, err := s.Sync(context.Background(), true /*lazy*/)
	require.NoError(t, err)
	assert.False(t, res.Downloaded)
}

func TestSync_ExplicitForcesRemoteCheck(t *testing.T) {
	cacheDir := t.TempDir()
	buildTinyDB(t, filepath.Join(cacheDir, "recipes.db"), "recipes-v1.0.0")
	// lastcheck very recent
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "recipes.lastcheck"),
		[]byte(time.Now().Format(time.RFC3339)), 0o644))

	srcDB := filepath.Join(t.TempDir(), "remote-recipes.db")
	buildTinyDB(t, srcDB, "recipes-v1.1.0") // newer
	srv := fakeReleasesServer(t, "recipes-v1.1.0", srcDB)
	defer srv.Close()

	s := NewSyncer(cacheDir, srv.URL+"/releases")
	res, err := s.Sync(context.Background(), false /*explicit*/)
	require.NoError(t, err)
	assert.Equal(t, "recipes-v1.1.0", res.Version)
	assert.True(t, res.Downloaded)
}

func TestSync_LazyNetworkFailureIsSilent(t *testing.T) {
	cacheDir := t.TempDir()
	// No cache file at all → lazy must still try (cache missing forces sync).
	s := NewSyncer(cacheDir, "http://127.0.0.1:1/releases")
	res, err := s.Sync(context.Background(), true /*lazy*/)
	// Lazy syncs swallow network errors; sync returns ok with Downloaded=false.
	require.NoError(t, err)
	assert.False(t, res.Downloaded)
}

func TestSync_ExplicitNetworkFailurePropagates(t *testing.T) {
	cacheDir := t.TempDir()
	s := NewSyncer(cacheDir, "http://127.0.0.1:1/releases")
	_, err := s.Sync(context.Background(), false /*explicit*/)
	require.Error(t, err)
}
