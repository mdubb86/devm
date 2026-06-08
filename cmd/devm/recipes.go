package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mdubb86/devm/internal/recipes"
)

var recipesCmd = &cobra.Command{
	Use:   "recipes",
	Short: "Tool integration recipe catalog (synced from GitHub releases)",
}

func openCachedQuery() (*recipes.Query, error) {
	dbPath := filepath.Join(recipes.CacheDir(), "recipes.db")
	if _, err := os.Stat(dbPath); err != nil {
		return nil, errors.New("no cached recipes — run `devm recipes sync`")
	}
	return recipes.Open(dbPath)
}

// lazyEnsureCache runs a lazy sync and ignores its result. Errors are
// silent — the subsequent open will surface a clear "no cached recipes"
// message if needed.
func lazyEnsureCache(ctx context.Context) {
	s := recipes.NewSyncer(recipes.CacheDir(), recipes.ReleasesURL())
	_, _ = s.Sync(ctx, true)
}

var recipesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List cached recipes (auto-syncs lazily once per day)",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		lazyEnsureCache(cmd.Context())
		category, _ := cmd.Flags().GetString("category")
		asJSON, _ := cmd.Flags().GetBool("json")
		q, err := openCachedQuery()
		if err != nil {
			return err
		}
		defer q.Close()
		all, err := q.List(category)
		if err != nil {
			return err
		}
		if asJSON {
			return json.NewEncoder(os.Stdout).Encode(all)
		}
		// Group by category, alphabetical within.
		byCat := map[string][]recipes.Recipe{}
		for _, r := range all {
			byCat[r.Category] = append(byCat[r.Category], r)
		}
		cats := make([]string, 0, len(byCat))
		for c := range byCat {
			cats = append(cats, c)
		}
		sort.Strings(cats)
		for _, c := range cats {
			fmt.Printf("%s\n", strings.ToUpper(c))
			for _, r := range byCat[c] {
				fmt.Printf("  %-30s %s\n", r.Name, r.Description)
			}
			fmt.Println()
		}
		return nil
	},
}

var recipesSearchCmd = &cobra.Command{
	Use:   "search <term>",
	Short: "Full-text search the recipe catalog",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		lazyEnsureCache(cmd.Context())
		limit, _ := cmd.Flags().GetInt("limit")
		q, err := openCachedQuery()
		if err != nil {
			return err
		}
		defer q.Close()
		hits, err := q.Search(args[0], limit)
		if err != nil {
			return err
		}
		for _, r := range hits {
			fmt.Printf("%-30s %s\n", r.Name, r.Description)
		}
		return nil
	},
}

var recipesGetCmd = &cobra.Command{
	Use:   "get <name>",
	Short: "Print a recipe's markdown body",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		lazyEnsureCache(cmd.Context())
		q, err := openCachedQuery()
		if err != nil {
			return err
		}
		defer q.Close()
		r, err := q.Get(args[0])
		if err != nil {
			return err
		}
		_, err = fmt.Print(strings.TrimRight(r.Content, "\n") + "\n")
		return err
	},
}

var recipesSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Force-fetch the latest recipes.db from GitHub",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		s := recipes.NewSyncer(recipes.CacheDir(), recipes.ReleasesURL())
		res, err := s.Sync(cmd.Context(), false)
		if err != nil {
			return err
		}
		if !res.Downloaded {
			fmt.Printf("already up to date (%s)\n", res.Version)
			return nil
		}
		fmt.Printf("synced %s\n", res.Version)
		return nil
	},
}

var recipesStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show cached recipes version and cache location",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		dir := recipes.CacheDir()
		fmt.Printf("cache dir:    %s\n", dir)
		q, err := openCachedQuery()
		if err != nil {
			fmt.Printf("local cache:  (none — run `devm recipes sync`)\n")
			return nil
		}
		defer q.Close()
		v, err := q.Version()
		if err == nil {
			fmt.Printf("local cache:  %s\n", v)
		}
		return nil
	},
}

func init() {
	recipesListCmd.Flags().String("category", "", "filter by category (lang, db, ai, ...)")
	recipesListCmd.Flags().Bool("json", false, "output as JSON")
	recipesSearchCmd.Flags().Int("limit", 20, "max results")
	recipesCmd.AddCommand(recipesListCmd)
	recipesCmd.AddCommand(recipesSearchCmd)
	recipesCmd.AddCommand(recipesGetCmd)
	recipesCmd.AddCommand(recipesSyncCmd)
	recipesCmd.AddCommand(recipesStatusCmd)
	rootCmd.AddCommand(recipesCmd)
}
