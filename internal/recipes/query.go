// Package recipes is the runtime layer for `devm recipes`: opens
// the synced SQLite database, exposes List/Search/Get, and (in
// sync.go) manages the lazy daily cache refresh.
package recipes

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// Recipe is one row from the recipes table, with the body content
// loaded for Get(). List() returns shallow copies with empty Content.
type Recipe struct {
	Name        string
	Category    string
	DisplayName string
	Description string
	Keywords    string
	Content     string
	Since       string
}

// Query wraps an open SQLite handle. Construct via Open(path); close
// with Close().
type Query struct {
	db *sql.DB
}

// Open opens the recipes DB. Returns an error if the file doesn't exist
// or fails a basic sanity check.
func Open(path string) (*Query, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("recipes: open %s: %w", path, err)
	}
	// Cheap sanity check: meta table exists.
	if _, err := db.QueryContext(context.Background(),
		"SELECT 1 FROM meta LIMIT 1"); err != nil {
		db.Close()
		return nil, fmt.Errorf("recipes: db at %s is missing meta: %w", path, err)
	}
	return &Query{db: db}, nil
}

func (q *Query) Close() error { return q.db.Close() }

// List returns recipes filtered by category (empty string = all),
// ordered by name. Content is NOT populated (callers should Get()
// when they need the body).
func (q *Query) List(category string) ([]Recipe, error) {
	ctx := context.Background()
	var (
		rows *sql.Rows
		err  error
	)
	if category == "" {
		rows, err = q.db.QueryContext(ctx,
			`SELECT name, category, display_name, description, keywords, since
			 FROM recipes ORDER BY name`)
	} else {
		rows, err = q.db.QueryContext(ctx,
			`SELECT name, category, display_name, description, keywords, since
			 FROM recipes WHERE category = ? ORDER BY name`, category)
	}
	if err != nil {
		return nil, fmt.Errorf("recipes: list: %w", err)
	}
	defer rows.Close()

	var out []Recipe
	for rows.Next() {
		var r Recipe
		var since sql.NullString
		if err := rows.Scan(&r.Name, &r.Category, &r.DisplayName,
			&r.Description, &r.Keywords, &since); err != nil {
			return nil, err
		}
		r.Since = since.String
		out = append(out, r)
	}
	return out, nil
}

// Search runs an FTS5 query over name + description + keywords + content.
// Returns up to limit results in FTS5's default rank order.
func (q *Query) Search(term string, limit int) ([]Recipe, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := q.db.QueryContext(context.Background(),
		`SELECT r.name, r.category, r.display_name, r.description, r.keywords, r.since
		 FROM recipes_fts f
		 JOIN recipes r ON r.name = f.name
		 WHERE recipes_fts MATCH ?
		 ORDER BY rank
		 LIMIT ?`,
		term, limit)
	if err != nil {
		return nil, fmt.Errorf("recipes: search %q: %w", term, err)
	}
	defer rows.Close()

	var out []Recipe
	for rows.Next() {
		var r Recipe
		var since sql.NullString
		if err := rows.Scan(&r.Name, &r.Category, &r.DisplayName,
			&r.Description, &r.Keywords, &since); err != nil {
			return nil, err
		}
		r.Since = since.String
		out = append(out, r)
	}
	return out, nil
}

// Get fetches a single recipe (with content). Returns error if not found.
func (q *Query) Get(name string) (Recipe, error) {
	var r Recipe
	var since sql.NullString
	err := q.db.QueryRowContext(context.Background(),
		`SELECT name, category, display_name, description, keywords, content, since
		 FROM recipes WHERE name = ?`, name).
		Scan(&r.Name, &r.Category, &r.DisplayName, &r.Description, &r.Keywords, &r.Content, &since)
	if err == sql.ErrNoRows {
		return Recipe{}, fmt.Errorf("recipes: unknown recipe %q", name)
	}
	if err != nil {
		return Recipe{}, err
	}
	r.Since = since.String
	return r, nil
}

// Version returns the meta.version string ('recipes-vX.Y.Z').
func (q *Query) Version() (string, error) {
	var v string
	err := q.db.QueryRowContext(context.Background(),
		`SELECT value FROM meta WHERE key = 'version'`).Scan(&v)
	if err != nil {
		return "", err
	}
	return v, nil
}
