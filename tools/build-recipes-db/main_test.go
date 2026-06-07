package main

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

const fixturePython = `---
name: tool/lang/python
category: lang
display_name: Python (uv)
description: uv-managed Python projects.
keywords: python uv
---

# Python body
Hello.
`

const fixtureNode = `---
name: tool/lang/node
category: lang
description: Node 22 LTS.
keywords: node npm
---

# Node body
World.
`

func writeFixtures(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "lang"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "lang", "python.md"), []byte(fixturePython), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "lang", "node.md"), []byte(fixtureNode), 0o644))
	return dir
}

func TestBuild_ProducesDBWithExpectedRows(t *testing.T) {
	src := writeFixtures(t)
	out := filepath.Join(t.TempDir(), "recipes.db")
	require.NoError(t, build(src, out, "recipes-v1.0.0"))

	db, err := sql.Open("sqlite", out)
	require.NoError(t, err)
	defer db.Close()

	var count int
	require.NoError(t, db.QueryRowContext(context.Background(),
		"SELECT count(*) FROM recipes").Scan(&count))
	assert.Equal(t, 2, count)

	var name string
	require.NoError(t, db.QueryRowContext(context.Background(),
		"SELECT name FROM recipes WHERE category = 'lang' AND name = 'tool/lang/python'").Scan(&name))
	assert.Equal(t, "tool/lang/python", name)
}

func TestBuild_FTSIndexesContent(t *testing.T) {
	src := writeFixtures(t)
	out := filepath.Join(t.TempDir(), "recipes.db")
	require.NoError(t, build(src, out, "recipes-v1.0.0"))

	db, err := sql.Open("sqlite", out)
	require.NoError(t, err)
	defer db.Close()

	rows, err := db.QueryContext(context.Background(),
		"SELECT name FROM recipes_fts WHERE recipes_fts MATCH 'python'")
	require.NoError(t, err)
	defer rows.Close()
	var got []string
	for rows.Next() {
		var n string
		require.NoError(t, rows.Scan(&n))
		got = append(got, n)
	}
	assert.Equal(t, []string{"tool/lang/python"}, got)
}

func TestBuild_MissingNameErrors(t *testing.T) {
	src := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(src, "bad.md"),
		[]byte("---\ncategory: lang\n---\nbody\n"), 0o644))
	err := build(src, filepath.Join(t.TempDir(), "out.db"), "v")
	require.Error(t, err)
}

func TestBuild_RecordsMeta(t *testing.T) {
	src := writeFixtures(t)
	out := filepath.Join(t.TempDir(), "recipes.db")
	require.NoError(t, build(src, out, "recipes-v1.2.3"))

	db, err := sql.Open("sqlite", out)
	require.NoError(t, err)
	defer db.Close()

	var v string
	require.NoError(t, db.QueryRowContext(context.Background(),
		"SELECT value FROM meta WHERE key = 'version'").Scan(&v))
	assert.Equal(t, "recipes-v1.2.3", v)
}
