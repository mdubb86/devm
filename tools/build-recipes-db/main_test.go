package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validRecipeBytes returns a minimal recipe body with every required
// frontmatter field set, so tests that focus on assets don't have to
// hand-craft one.
func validRecipeBytes(name string) []byte {
	return []byte(fmt.Sprintf(
		"---\nname: %s\ncategory: tool\ndisplay_name: Foo\ndescription: desc\nkeywords: k\nsince: recipes-v2.0.0\n---\n\nbody",
		name,
	))
}

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
	require.NoError(t, build(src, out, "recipes-v2.0.0"))

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
	require.NoError(t, build(src, out, "recipes-v2.0.0"))

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

func TestBuild_IncludesAssets(t *testing.T) {
	src := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(src, "tool"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "tool", "foo.md"),
		[]byte("---\nname: tool/foo\ncategory: tool\ndisplay_name: Foo\ndescription: desc\nkeywords: k\nsince: recipes-v2.0.0\n---\n\nbody"),
		0o644))
	assetsDir := filepath.Join(src, "tool", "foo-assets")
	require.NoError(t, os.MkdirAll(filepath.Join(assetsDir, "skills"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(assetsDir, "skills", "one.md"), []byte("alpha"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(assetsDir, "top.txt"), []byte("beta"), 0o644))

	out := filepath.Join(t.TempDir(), "recipes.db")
	require.NoError(t, build(src, out, "recipes-v2.0.0"))

	db, err := sql.Open("sqlite", out)
	require.NoError(t, err)
	defer db.Close()

	rows, err := db.Query(
		`SELECT path, content, size, mode FROM assets WHERE recipe_name = ? ORDER BY path`,
		"tool/foo",
	)
	require.NoError(t, err)
	defer rows.Close()

	type row struct {
		Path    string
		Content []byte
		Size    int64
		Mode    uint32
	}
	var got []row
	for rows.Next() {
		var r row
		require.NoError(t, rows.Scan(&r.Path, &r.Content, &r.Size, &r.Mode))
		got = append(got, r)
	}
	require.Len(t, got, 2)
	assert.Equal(t, "skills/one.md", got[0].Path)
	assert.Equal(t, []byte("alpha"), got[0].Content)
	assert.Equal(t, int64(5), got[0].Size)
	assert.Equal(t, "top.txt", got[1].Path)
}

func TestBuild_EmptyAssetsDir_NoRows(t *testing.T) {
	src := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(src, "tool"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "tool", "foo.md"),
		validRecipeBytes("tool/foo"), 0o644))
	// Empty assets dir — must not error, must not produce rows.
	require.NoError(t, os.MkdirAll(filepath.Join(src, "tool", "foo-assets"), 0o755))

	out := filepath.Join(t.TempDir(), "recipes.db")
	require.NoError(t, build(src, out, "recipes-v2.0.0"))

	db, err := sql.Open("sqlite", out)
	require.NoError(t, err)
	defer db.Close()

	var count int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM assets WHERE recipe_name = ?`, "tool/foo").Scan(&count))
	assert.Zero(t, count)
}

func TestBuild_RejectsSymlinkEscape(t *testing.T) {
	src := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(src, "tool"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "tool", "foo.md"),
		validRecipeBytes("tool/foo"), 0o644))
	assetsDir := filepath.Join(src, "tool", "foo-assets")
	require.NoError(t, os.MkdirAll(assetsDir, 0o755))
	// Create a target outside the source tree and a symlink into it.
	outside := filepath.Join(t.TempDir(), "victim.txt")
	require.NoError(t, os.WriteFile(outside, []byte("secret"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(assetsDir, "escape.txt")))

	out := filepath.Join(t.TempDir(), "recipes.db")
	err := build(src, out, "recipes-v2.0.0")
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "escap")
}

func TestBuild_RejectsAssetsDirSymlinkEscape(t *testing.T) {
	// The <recipe>-assets directory itself is a symlink pointing to a
	// tree outside the recipe source. build() must reject before
	// enumerating anything under the target.
	src := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(src, "tool"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "tool", "foo.md"),
		validRecipeBytes("tool/foo"), 0o644))

	outsideDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outsideDir, "leak.txt"),
		[]byte("secret"), 0o600))
	require.NoError(t, os.Symlink(outsideDir, filepath.Join(src, "tool", "foo-assets")))

	out := filepath.Join(t.TempDir(), "recipes.db")
	err := build(src, out, "recipes-v2.0.0")
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "outside recipe dir")
}

func TestBuild_AcceptsDotDotPrefixedFilename(t *testing.T) {
	// A filesystem-legal filename that happens to start with ".." (e.g.
	// "..README.md") does not traverse anywhere and must be accepted;
	// the escape check keys on the ".." path segment, not the two-char
	// prefix.
	src := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(src, "tool"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "tool", "foo.md"),
		validRecipeBytes("tool/foo"), 0o644))
	assetsDir := filepath.Join(src, "tool", "foo-assets")
	require.NoError(t, os.MkdirAll(assetsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(assetsDir, "..README.md"),
		[]byte("hello"), 0o644))

	out := filepath.Join(t.TempDir(), "recipes.db")
	require.NoError(t, build(src, out, "recipes-v2.0.0"))

	db, err := sql.Open("sqlite", out)
	require.NoError(t, err)
	defer db.Close()

	var got string
	require.NoError(t, db.QueryRow(
		`SELECT path FROM assets WHERE recipe_name = ? AND path = ?`,
		"tool/foo", "..README.md").Scan(&got))
	assert.Equal(t, "..README.md", got)
}

func TestBuild_RejectsInvalidAssetPath(t *testing.T) {
	// A file whose relative path would be rejected by validAssetPath —
	// e.g. a file whose name contains a backslash (rare on unix but
	// filesystem-legal). Simulate by writing a filename with a backslash
	// and checking build errors.
	src := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(src, "tool"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "tool", "foo.md"),
		validRecipeBytes("tool/foo"), 0o644))
	assetsDir := filepath.Join(src, "tool", "foo-assets")
	require.NoError(t, os.MkdirAll(assetsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(assetsDir, "a\\b.md"), []byte("x"), 0o644))

	out := filepath.Join(t.TempDir(), "recipes.db")
	err := build(src, out, "recipes-v2.0.0")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid asset path")
}

// TestBuild_RejectsDuplicateAssetPaths pins that two files resolving
// to the same relative asset path (e.g. via a symlink pointing at
// an already-present sibling) fail loudly rather than silently
// clobbering. The assets table's PRIMARY KEY (recipe_name, path)
// enforces this at insert; the test simulates it via a symlink
// pointing at a real file in the same dir under a duplicate name.
func TestBuild_RejectsDuplicateAssetPaths(t *testing.T) {
	src := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(src, "tool"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "tool", "foo.md"),
		validRecipeBytes("tool/foo"), 0o644))
	assetsDir := filepath.Join(src, "tool", "foo-assets")
	require.NoError(t, os.MkdirAll(assetsDir, 0o755))
	// Two files whose EvalSymlinks-resolved paths land at the same
	// content but via different names. Since PK is (recipe_name, path)
	// and path is filepath.Rel(assetsRoot, resolvedPath), a symlink
	// pointing to a real sibling produces two entries with the SAME
	// resolved relative path — insert #2 fails on the PK constraint.
	require.NoError(t, os.WriteFile(filepath.Join(assetsDir, "real.md"), []byte("original"), 0o644))
	require.NoError(t, os.Symlink("real.md", filepath.Join(assetsDir, "alias.md")))

	out := filepath.Join(t.TempDir(), "recipes.db")
	err := build(src, out, "recipes-v2.0.0")
	require.Error(t, err, "duplicate resolved asset paths must fail the build")
	assert.Contains(t, err.Error(), "insert asset")
}

// TestBuild_RejectsSymlinkedDirectoryInsideAssetsDir pins the behavior
// of a directory-typed entry inside <name>-assets that's actually a
// symlink to another directory. WalkDir walks the target's contents;
// if the target is inside the assets tree, contents get ingested
// normally; if outside, the per-entry escape check must catch it
// exactly like a symlinked file would.
func TestBuild_RejectsSymlinkedDirectoryInsideAssetsDir(t *testing.T) {
	src := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(src, "tool"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "tool", "foo.md"),
		validRecipeBytes("tool/foo"), 0o644))
	assetsDir := filepath.Join(src, "tool", "foo-assets")
	require.NoError(t, os.MkdirAll(assetsDir, 0o755))
	// Plant a directory OUTSIDE the recipe tree with a file inside.
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "leak.md"), []byte("secret"), 0o600))
	// Symlink the assets dir's "skills" entry to the outside directory.
	require.NoError(t, os.Symlink(outside, filepath.Join(assetsDir, "skills")))

	out := filepath.Join(t.TempDir(), "recipes.db")
	err := build(src, out, "recipes-v2.0.0")
	require.Error(t, err, "symlinked directory pointing outside the recipe tree must be rejected")
	assert.Contains(t, strings.ToLower(err.Error()), "escap")
}

func TestBuild_RecordsMeta(t *testing.T) {
	src := writeFixtures(t)
	out := filepath.Join(t.TempDir(), "recipes.db")
	require.NoError(t, build(src, out, "recipes-v2.1.3"))

	db, err := sql.Open("sqlite", out)
	require.NoError(t, err)
	defer db.Close()

	var v string
	require.NoError(t, db.QueryRowContext(context.Background(),
		"SELECT value FROM meta WHERE key = 'version'").Scan(&v))
	assert.Equal(t, "recipes-v2.1.3", v)
}
