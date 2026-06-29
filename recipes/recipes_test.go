package recipes_test

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

// ---------------------------------------------------------------------------
// Retired-term scan
// ---------------------------------------------------------------------------

// recipeRetiredTerms mirrors the canonical list in
// internal/skills/drift_test.go. Duplicated here to avoid a circular
// import (recipes_test is an external test package; internal/skills
// test helpers are not importable). Keep the two lists in sync.
var recipeRetiredTerms = []string{
	"sbx",
	"wrap-fg", "wrap-bg",
	"install-all-ok", "startup-all-ok",
	"allowed_domains",
	"sandbox_name",
	"kit-policy", "kit policy",
	"anchor process", "anchor-alive",
}

// scanRecipeForRetiredTerms returns the list of retired terms found in
// body using case-insensitive whole-word matching. Mirrors the helper
// in internal/skills/drift_test.go.
func scanRecipeForRetiredTerms(body string, terms []string) []string {
	var hits []string
	lower := strings.ToLower(body)
	for _, term := range terms {
		re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(term) + `\b`)
		if re.MatchString(lower) {
			hits = append(hits, term)
		}
	}
	return hits
}

// TestNoRetiredTermsInRecipes fails when any recipe markdown file
// contains a retired term from the pre-refactor vocabulary.
func TestNoRetiredTermsInRecipes(t *testing.T) {
	repoRoot, err := filepath.Abs("..")
	require.NoError(t, err)

	recipesDir := filepath.Join(repoRoot, "recipes")
	err = filepath.Walk(recipesDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		hits := scanRecipeForRetiredTerms(string(content), recipeRetiredTerms)
		assert.Empty(t, hits,
			"recipe %q contains retired terms %v — update the recipe to use current terminology",
			path, hits)
		return nil
	})
	require.NoError(t, err)
}
