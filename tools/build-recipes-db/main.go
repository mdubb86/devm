// Command build-recipes-db walks a directory of markdown recipe files
// and produces a SQLite database with an FTS5 index over their content.
// Used by .github/workflows/recipes-release.yml on a gated tag push.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mdubb86/devm/internal/recipes"
	"gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"
)

func main() {
	src := flag.String("src", "recipes", "source directory with category subdirs")
	out := flag.String("out", "recipes.db", "output SQLite path")
	version := flag.String("version", "recipes-v0.0.0", "release version stamped into meta")
	flag.Parse()

	if err := build(*src, *out, *version); err != nil {
		log.Fatalf("build: %v", err)
	}
}

type recipe struct {
	Name        string
	Category    string
	DisplayName string
	Description string
	Keywords    string
	Since       string
	Content     string
}

func build(srcDir, outPath, version string) error {
	if err := os.RemoveAll(outPath); err != nil {
		return fmt.Errorf("remove existing %s: %w", outPath, err)
	}

	db, err := sql.Open("sqlite", outPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", outPath, err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := recipes.InitSchema(ctx, db); err != nil {
		return err
	}

	var parsed []parsedRecipe
	err = filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// A sibling <name>-assets/ dir holds asset files, not
			// recipes; ingestAssets walks it after the recipe is
			// inserted, so the recipe walk must not descend into it.
			if strings.HasSuffix(d.Name(), "-assets") {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".md" {
			return nil
		}
		if filepath.Base(path) == "README.md" {
			return nil
		}
		r, err := parseRecipe(srcDir, path)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		parsed = append(parsed, parsedRecipe{recipe: r, path: path})
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk %s: %w", srcDir, err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	builtAt := time.Now().Unix()
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO meta (key, value) VALUES (?, ?), (?, ?), (?, ?)",
		"version", version,
		"built_at", fmt.Sprintf("%d", builtAt),
		"recipe_count", fmt.Sprintf("%d", len(parsed)),
	); err != nil {
		return err
	}

	for _, pr := range parsed {
		r := pr.recipe
		_, err := tx.ExecContext(ctx,
			`INSERT INTO recipes
			   (name, category, display_name, description, keywords, content, since, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			r.Name, r.Category, r.DisplayName, r.Description, r.Keywords, r.Content, r.Since,
			builtAt,
		)
		if err != nil {
			return fmt.Errorf("insert %s: %w", r.Name, err)
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO recipes_fts (name, display_name, description, keywords, content)
			 VALUES (?, ?, ?, ?, ?)`,
			r.Name, r.DisplayName, r.Description, r.Keywords, r.Content,
		)
		if err != nil {
			return fmt.Errorf("fts insert %s: %w", r.Name, err)
		}
		if err := ingestAssets(ctx, tx, r, pr.path); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// parsedRecipe pairs a parsed recipe with the source path we read it
// from, so the tx loop can find its sibling <name>-assets/ dir.
type parsedRecipe struct {
	recipe recipe
	path   string
}

// ingestAssets walks the sibling <name>-assets/ directory of the recipe
// at recipePath and inserts one row per regular file into the assets
// table on the given transaction. Symlinks that resolve outside the
// assets root are rejected, as are paths that fail
// recipes.ValidAssetPath. A missing or empty dir is a no-op.
func ingestAssets(ctx context.Context, tx *sql.Tx, r recipe, recipePath string) error {
	recipeBase := strings.TrimSuffix(filepath.Base(recipePath), ".md")
	assetsDir := filepath.Join(filepath.Dir(recipePath), recipeBase+"-assets")

	realAssetsDir, err := filepath.EvalSymlinks(assetsDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("assets dir %s: %w", assetsDir, err)
	}
	// Reject the assets dir itself being a symlink to somewhere outside
	// the recipe tree — otherwise the walk enumerates the target and
	// stores those files as assets, with per-entry Rel checks against
	// the resolved target giving a false all-clear.
	realRecipeDir, err := filepath.EvalSymlinks(filepath.Dir(recipePath))
	if err != nil {
		return fmt.Errorf("recipe dir %s: %w", recipePath, err)
	}
	if rel, err := filepath.Rel(realRecipeDir, realAssetsDir); err != nil ||
		rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(rel) {
		return fmt.Errorf("asset dir %s resolves outside recipe dir (resolved: %s, rel: %s)", assetsDir, realAssetsDir, rel)
	}

	return filepath.WalkDir(realAssetsDir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		// Resolve each entry so a symlink pointing outside the assets
		// root is caught by the Rel check below.
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			return fmt.Errorf("evalsymlinks %s: %w", p, err)
		}
		rel, err := filepath.Rel(realAssetsDir, real)
		if err != nil {
			return fmt.Errorf("rel %s: %w", real, err)
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return fmt.Errorf("asset %s escapes recipe assets dir (resolved: %s)", p, real)
		}
		relSlash := filepath.ToSlash(rel)
		if err := recipes.ValidAssetPath(relSlash); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		info, err := os.Stat(real)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(real)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO assets(recipe_name, path, content, size, mode) VALUES (?, ?, ?, ?, ?)`,
			r.Name, relSlash, body, info.Size(), uint32(info.Mode().Perm()),
		); err != nil {
			return fmt.Errorf("insert asset %s/%s: %w", r.Name, relSlash, err)
		}
		return nil
	})
}

func parseRecipe(srcDir, path string) (recipe, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return recipe{}, err
	}
	// Normalize CRLF → LF so Windows checkouts without core.autocrlf
	// don't confuse the frontmatter parser.
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return recipe{}, fmt.Errorf("missing frontmatter")
	}
	rest := text[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return recipe{}, fmt.Errorf("missing frontmatter closer")
	}
	frontmatter := rest[:end]
	body := strings.TrimLeft(rest[end+len("\n---\n"):], "\n")

	var meta struct {
		Name        string `yaml:"name"`
		Category    string `yaml:"category"`
		DisplayName string `yaml:"display_name"`
		Description string `yaml:"description"`
		Keywords    string `yaml:"keywords"`
		Since       string `yaml:"since"`
	}
	if err := yaml.Unmarshal([]byte(frontmatter), &meta); err != nil {
		return recipe{}, fmt.Errorf("yaml: %w", err)
	}
	if meta.Name == "" {
		return recipe{}, fmt.Errorf("missing required field 'name'")
	}
	if meta.Category == "" {
		return recipe{}, fmt.Errorf("missing required field 'category'")
	}
	if meta.Description == "" {
		return recipe{}, fmt.Errorf("missing required field 'description'")
	}
	if meta.Keywords == "" {
		return recipe{}, fmt.Errorf("missing required field 'keywords'")
	}
	if meta.DisplayName == "" {
		meta.DisplayName = meta.Name
	}
	return recipe{
		Name:        meta.Name,
		Category:    meta.Category,
		DisplayName: meta.DisplayName,
		Description: meta.Description,
		Keywords:    meta.Keywords,
		Since:       meta.Since,
		Content:     body,
	}, nil
}
