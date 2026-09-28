package main

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mdubb86/devm/internal/recipes"
)

// buildAssetFixture writes a recipes DB under a fresh temp dir with the
// given recipes and their assets, points DEVM_RECIPES_CACHE_DIR at it,
// and returns the directory path. Assets are stored with mode 0o644.
// A nil fixtures map creates only the empty schema (no recipe rows).
func buildAssetFixture(t *testing.T, fixtures map[string]map[string][]byte) string {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "recipes.db")

	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, recipes.InitSchema(ctx, db))
	_, err = db.ExecContext(ctx,
		`INSERT INTO meta VALUES ('version', 'recipes-v2.0.0')`)
	require.NoError(t, err)

	for name, assets := range fixtures {
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

	t.Setenv("DEVM_RECIPES_CACHE_DIR", dir)
	return dir
}

// runRootCmd invokes rootCmd with args, capturing stdout and stderr into
// separate buffers. rootCmd has SilenceErrors=true so any RunE-returned
// error surfaces only via the return value; the CLI's main() prints it
// to os.Stderr, and tests check err.Error() to reason about that text.
func runRootCmd(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	rootCmd.SetOut(&outBuf)
	rootCmd.SetErr(&errBuf)
	rootCmd.SetArgs(args)
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})
	err = rootCmd.Execute()
	return outBuf.String(), errBuf.String(), err
}

func TestRecipesAssetLs_HappyPathSortedTabSeparated(t *testing.T) {
	buildAssetFixture(t, map[string]map[string][]byte{
		"tool/foo": {
			"z/last.md":   []byte("zzz"),
			"a/first.md":  []byte("a"),
			"m/middle.md": []byte("mm"),
		},
	})

	stdout, _, err := runRootCmd(t, "recipes", "asset", "ls", "tool/foo")
	require.NoError(t, err)

	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	require.Len(t, lines, 3, "expected three asset lines, got: %q", stdout)
	assert.Equal(t, "1\t644\ta/first.md", lines[0])
	assert.Equal(t, "2\t644\tm/middle.md", lines[1])
	assert.Equal(t, "3\t644\tz/last.md", lines[2])
}

func TestRecipesAssetLs_UnknownRecipe_Errors(t *testing.T) {
	buildAssetFixture(t, nil)

	stdout, _, err := runRootCmd(t, "recipes", "asset", "ls", "tool/nope")
	require.Error(t, err)
	assert.Empty(t, stdout)
	assert.Contains(t, err.Error(), "tool/nope",
		"stderr must name the unknown recipe")
}

func TestRecipesAssetLs_EmptyAssets_Succeeds(t *testing.T) {
	buildAssetFixture(t, map[string]map[string][]byte{
		"tool/foo": {},
	})

	stdout, _, err := runRootCmd(t, "recipes", "asset", "ls", "tool/foo")
	require.NoError(t, err)
	assert.Empty(t, stdout, "recipe with no assets emits no lines")
}

func TestRecipesAssetGet_HappyPath_RawBytes(t *testing.T) {
	body := []byte("skill body with\x00a NUL and no trailing newline")
	buildAssetFixture(t, map[string]map[string][]byte{
		"tool/foo": {"skills/x.md": body},
	})

	stdout, _, err := runRootCmd(t, "recipes", "asset", "get", "tool/foo", "skills/x.md")
	require.NoError(t, err)
	assert.Equal(t, string(body), stdout, "stdout must be byte-identical to fixture")
}

func TestRecipesAssetGet_InvalidPath_Errors(t *testing.T) {
	buildAssetFixture(t, map[string]map[string][]byte{
		"tool/foo": {"skills/x.md": []byte("y")},
	})

	stdout, _, err := runRootCmd(t, "recipes", "asset", "get", "tool/foo", "../etc/passwd")
	require.Error(t, err)
	assert.Empty(t, stdout)
	assert.Contains(t, strings.ToLower(err.Error()), "invalid",
		"stderr must mention \"invalid\" for a bad path")
}

func TestRecipesAssetGet_UnknownAsset_Errors(t *testing.T) {
	buildAssetFixture(t, map[string]map[string][]byte{
		"tool/foo": {"skills/x.md": []byte("y")},
	})

	stdout, _, err := runRootCmd(t, "recipes", "asset", "get", "tool/foo", "skills/other.md")
	require.Error(t, err)
	assert.Empty(t, stdout)
	assert.Contains(t, err.Error(), "skills/other.md",
		"stderr must name the missing asset path")
}
