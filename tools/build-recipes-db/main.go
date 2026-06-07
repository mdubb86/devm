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
	if err := initSchema(ctx, db); err != nil {
		return err
	}

	var recipes []recipe
	err = filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
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
		recipes = append(recipes, r)
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
		"recipe_count", fmt.Sprintf("%d", len(recipes)),
	); err != nil {
		return err
	}

	for _, r := range recipes {
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
	}
	return tx.Commit()
}

func initSchema(ctx context.Context, db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE meta (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE recipes (
			name         TEXT PRIMARY KEY,
			category     TEXT NOT NULL,
			display_name TEXT NOT NULL,
			description  TEXT NOT NULL,
			keywords     TEXT NOT NULL,
			content      TEXT NOT NULL,
			since        TEXT,
			updated_at   INTEGER NOT NULL
		)`,
		`CREATE INDEX idx_recipes_category ON recipes(category)`,
		`CREATE VIRTUAL TABLE recipes_fts USING fts5(
			name, display_name, description, keywords, content,
			tokenize = 'porter'
		)`,
	}
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("init schema (%s): %w", strings.SplitN(s, "\n", 2)[0], err)
		}
	}
	return nil
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
