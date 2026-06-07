package recipes_test

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAllRepoRecipesBuildSuccessfully runs the actual build tool over
// the in-repo recipes/ tree. Any missing frontmatter, bad YAML, or
// name/category mismatch surfaces here.
func TestAllRepoRecipesBuildSuccessfully(t *testing.T) {
	repoRoot, err := filepath.Abs("..")
	require.NoError(t, err)

	out := filepath.Join(t.TempDir(), "recipes.db")
	cmd := exec.Command("go", "run", "./tools/build-recipes-db",
		"-src", filepath.Join(repoRoot, "recipes"),
		"-out", out,
		"-version", "recipes-v0.0.0-test")
	cmd.Dir = repoRoot
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, output)
	}

	st, err := os.Stat(out)
	require.NoError(t, err)
	require.Greater(t, st.Size(), int64(1024))

	db, err := sql.Open("sqlite", out)
	require.NoError(t, err)
	defer db.Close()

	rows, err := db.QueryContext(context.Background(),
		"SELECT name, category FROM recipes ORDER BY name")
	require.NoError(t, err)
	defer rows.Close()

	var checked int
	for rows.Next() {
		var name, category string
		require.NoError(t, rows.Scan(&name, &category))

		// name must start with tool/<category>/
		expectedPrefix := "tool/" + category + "/"
		assert.True(t, strings.HasPrefix(name, expectedPrefix),
			"recipe %q has category %q but name doesn't start with %q",
			name, category, expectedPrefix)
		checked++
	}
	require.Greater(t, checked, 0, "at least one recipe must exist")
}
