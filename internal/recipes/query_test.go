package recipes

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeFixtureDB writes a minimal SQLite DB with two recipes for the
// query-layer tests. Mirrors the schema build-recipes-db produces.
func makeFixtureDB(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "recipes.db")

	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer db.Close()

	ctx := context.Background()
	stmts := []string{
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE TABLE recipes (
			name TEXT PRIMARY KEY, category TEXT NOT NULL,
			display_name TEXT NOT NULL, description TEXT NOT NULL,
			keywords TEXT NOT NULL, content TEXT NOT NULL,
			since TEXT, updated_at INTEGER NOT NULL
		)`,
		`CREATE INDEX idx_recipes_category ON recipes(category)`,
		`CREATE VIRTUAL TABLE recipes_fts USING fts5(
			name, display_name, description, keywords, content,
			tokenize = 'porter'
		)`,
		`INSERT INTO meta VALUES ('version', 'recipes-v1.0.0')`,
		`INSERT INTO recipes VALUES
			('tool/lang/python', 'lang', 'Python (uv)', 'uv-managed Python',
			 'python uv pyproject', '# Python content', 'recipes-v1.0.0', 1),
			('tool/db/postgres', 'db', 'PostgreSQL', 'Postgres service',
			 'postgres psql db', '# Postgres content', 'recipes-v1.0.0', 1)`,
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
	assert.Equal(t, "recipes-v1.0.0", v)
}

// Trivial smoke that the helper writes a real file.
func TestFixtureExists(t *testing.T) {
	dbPath := makeFixtureDB(t)
	_, err := os.Stat(dbPath)
	require.NoError(t, err)
}
