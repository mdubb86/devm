package recipes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildFixtureDB writes a schema-only recipes DB via the production
// InitSchema, stamps meta.version = recipes-v2.0.0, and closes.
// Used by tests asserting the shape callers can rely on.
func buildFixtureDB(dbPath string) error {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	ctx := context.Background()
	if err := InitSchema(ctx, db); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO meta VALUES ('version', 'recipes-v2.0.0')`); err != nil {
		return fmt.Errorf("buildFixtureDB: stamp version: %w", err)
	}
	return nil
}

// makeFixtureDB writes a minimal SQLite DB with two recipes for the
// query-layer tests. Uses the production InitSchema, then inserts the
// fixture rows.
func makeFixtureDB(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "recipes.db")
	require.NoError(t, buildFixtureDB(dbPath))

	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer db.Close()

	ctx := context.Background()
	stmts := []string{
		`INSERT INTO recipes VALUES
			('tool/lang/python', 'lang', 'Python (uv)', 'uv-managed Python',
			 'python uv pyproject', '# Python content', 'recipes-v2.0.0', 1),
			('tool/db/postgres', 'db', 'PostgreSQL', 'Postgres service',
			 'postgres psql db', '# Postgres content', 'recipes-v2.0.0', 1)`,
		`INSERT INTO recipes_fts (name, display_name, description, keywords, content) VALUES
			('tool/lang/python', 'Python (uv)', 'uv-managed Python', 'python uv pyproject', '# Python content'),
			('tool/db/postgres', 'PostgreSQL', 'Postgres service', 'postgres psql db', '# Postgres content')`,
	}
	for _, s := range stmts {
		_, err := db.ExecContext(ctx, s)
		require.NoError(t, err, s)
	}
	return dbPath
}

func TestList_ReturnsAll(t *testing.T) {
	dbPath := makeFixtureDB(t)
	q, err := Open(dbPath)
	require.NoError(t, err)
	defer q.Close()

	all, err := q.List("")
	require.NoError(t, err)
	require.Len(t, all, 2)
}

func TestList_FilterByCategory(t *testing.T) {
	dbPath := makeFixtureDB(t)
	q, err := Open(dbPath)
	require.NoError(t, err)
	defer q.Close()

	lang, err := q.List("lang")
	require.NoError(t, err)
	require.Len(t, lang, 1)
	assert.Equal(t, "tool/lang/python", lang[0].Name)
}

func TestSearch_RanksByRelevance(t *testing.T) {
	dbPath := makeFixtureDB(t)
	q, err := Open(dbPath)
	require.NoError(t, err)
	defer q.Close()

	hits, err := q.Search("python", 10)
	require.NoError(t, err)
	require.Len(t, hits, 1)
	assert.Equal(t, "tool/lang/python", hits[0].Name)
}

func TestGet_ReturnsContent(t *testing.T) {
	dbPath := makeFixtureDB(t)
	q, err := Open(dbPath)
	require.NoError(t, err)
	defer q.Close()

	r, err := q.Get("tool/db/postgres")
	require.NoError(t, err)
	assert.Contains(t, r.Content, "Postgres content")
}

func TestGet_UnknownReturnsError(t *testing.T) {
	dbPath := makeFixtureDB(t)
	q, err := Open(dbPath)
	require.NoError(t, err)
	defer q.Close()

	_, err = q.Get("tool/nope/missing")
	require.Error(t, err)
}

func TestOpen_FileMissingReturnsError(t *testing.T) {
	_, err := Open(filepath.Join(t.TempDir(), "no-such-file.db"))
	require.Error(t, err)
}

func TestMetaVersion(t *testing.T) {
	dbPath := makeFixtureDB(t)
	q, err := Open(dbPath)
	require.NoError(t, err)
	defer q.Close()

	v, err := q.Version()
	require.NoError(t, err)
	assert.Equal(t, "recipes-v2.0.0", v)
}

// Trivial smoke that the helper writes a real file.
func TestFixtureExists(t *testing.T) {
	dbPath := makeFixtureDB(t)
	_, err := os.Stat(dbPath)
	require.NoError(t, err)
}

// openFixtureWithAssets creates a fresh recipes DB, inserts the given
// recipes and their assets directly via SQL, and returns an open Query.
// A nil map creates only meta + empty tables. An empty inner map creates
// the recipe row with zero assets.
func openFixtureWithAssets(t *testing.T, recipes map[string]map[string][]byte) *Query {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "recipes.db")
	require.NoError(t, buildFixtureDB(dbPath))

	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	for name, assets := range recipes {
		_, err := db.ExecContext(ctx,
			`INSERT INTO recipes(name, category, display_name, description, keywords, content, since, updated_at)
			 VALUES (?, 'tool', ?, '', '', '', 'recipes-v2.0.0', 1)`,
			name, name)
		require.NoError(t, err)
		for path, content := range assets {
			_, err := db.ExecContext(ctx,
				`INSERT INTO assets(recipe_name, path, content, size, mode)
				 VALUES (?, ?, ?, ?, ?)`,
				name, path, content, int64(len(content)), 0o644)
			require.NoError(t, err)
		}
	}
	require.NoError(t, db.Close())

	q, err := Open(dbPath)
	require.NoError(t, err)
	return q
}

func TestListAssets_ReturnsSortedEmptyForNoAssets(t *testing.T) {
	q := openFixtureWithAssets(t, map[string]map[string][]byte{
		"tool/foo": {}, // recipe exists, zero assets
	})
	defer q.Close()

	got, err := q.ListAssets(context.Background(), "tool/foo")
	require.NoError(t, err)
	assert.Empty(t, got, "recipe with no assets returns empty listing (not nil err)")
}

func TestListAssets_ReturnsSortedByPath(t *testing.T) {
	q := openFixtureWithAssets(t, map[string]map[string][]byte{
		"tool/bar": {
			"z/last.md":   []byte("z"),
			"a/first.md":  []byte("a"),
			"m/middle.md": []byte("m"),
		},
	})
	defer q.Close()

	got, err := q.ListAssets(context.Background(), "tool/bar")
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, "a/first.md", got[0].Path)
	assert.Equal(t, "m/middle.md", got[1].Path)
	assert.Equal(t, "z/last.md", got[2].Path)
	assert.Equal(t, int64(1), got[0].Size)
	assert.NotZero(t, got[0].Mode)
}

func TestListAssets_MissingRecipe_Error(t *testing.T) {
	q := openFixtureWithAssets(t, nil)
	defer q.Close()

	_, err := q.ListAssets(context.Background(), "tool/nonexistent")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRecipeNotFound))
}

func TestGetAsset_HappyPath(t *testing.T) {
	body := []byte("skill content")
	q := openFixtureWithAssets(t, map[string]map[string][]byte{
		"tool/foo": {"skills/x.md": body},
	})
	defer q.Close()

	got, err := q.GetAsset(context.Background(), "tool/foo", "skills/x.md")
	require.NoError(t, err)
	assert.Equal(t, body, got)
}

func TestGetAsset_MissingAsset_Error(t *testing.T) {
	q := openFixtureWithAssets(t, map[string]map[string][]byte{
		"tool/foo": {"skills/x.md": []byte("y")},
	})
	defer q.Close()

	_, err := q.GetAsset(context.Background(), "tool/foo", "skills/other.md")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrAssetNotFound))
}

func TestGetAsset_MissingRecipe_Error(t *testing.T) {
	q := openFixtureWithAssets(t, nil)
	defer q.Close()

	_, err := q.GetAsset(context.Background(), "tool/nope", "skills/x.md")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRecipeNotFound))
}

func TestGetAsset_InvalidPath_Error(t *testing.T) {
	q := openFixtureWithAssets(t, map[string]map[string][]byte{
		"tool/foo": {"skills/x.md": []byte("y")},
	})
	defer q.Close()

	_, err := q.GetAsset(context.Background(), "tool/foo", "../etc/passwd")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidAssetPath))
}

func TestOpen_HasAssetsTableAndV2Version(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "recipes.db")
	require.NoError(t, buildFixtureDB(dbPath))

	q, err := Open(dbPath)
	require.NoError(t, err)
	defer q.Close()

	var version string
	require.NoError(t, q.db.QueryRow("SELECT value FROM meta WHERE key = 'version'").Scan(&version))
	assert.Equal(t, "recipes-v2.0.0", version)

	var name string
	require.NoError(t, q.db.QueryRow(
		"SELECT name FROM sqlite_schema WHERE type='table' AND name='assets'").Scan(&name))
	assert.Equal(t, "assets", name)

	rows, err := q.db.Query("PRAGMA table_info(assets)")
	require.NoError(t, err)
	defer rows.Close()
	cols := map[string]string{}
	for rows.Next() {
		var (
			cid       int
			cName     string
			cType     string
			notnull   int
			dfltValue sql.NullString
			pk        int
		)
		require.NoError(t, rows.Scan(&cid, &cName, &cType, &notnull, &dfltValue, &pk))
		cols[cName] = cType
	}
	assert.Equal(t, "TEXT", cols["recipe_name"])
	assert.Equal(t, "TEXT", cols["path"])
	assert.Equal(t, "BLOB", cols["content"])
	assert.Equal(t, "INTEGER", cols["size"])
	assert.Equal(t, "INTEGER", cols["mode"])
}
