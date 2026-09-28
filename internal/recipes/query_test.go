package recipes

import (
	"context"
	"database/sql"
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
