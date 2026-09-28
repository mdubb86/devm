// recipes_handler wires the guest-facing /recipes/* HTTP API on the
// per-project softnet listener at 192.168.127.1:82. Handlers are thin:
// they parse query params, open a fresh Query on the cached recipes.db,
// dispatch to the recipes package, and translate its typed sentinels to
// HTTP status codes. Registered from serveProposeListener.
package serviceapi

import (
	"errors"
	"log"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/mdubb86/devm/internal/daemonlog"
	"github.com/mdubb86/devm/internal/recipes"
)

// registerRecipesRoutes wires the four /recipes/* endpoints onto mux.
// openQuery is called per-request to obtain a fresh Query on the
// daemon's cached recipes.db (or a fake in tests). The handler closes
// the returned Query before returning.
func registerRecipesRoutes(mux *http.ServeMux, openQuery func() (*recipes.Query, error)) {
	mux.HandleFunc("/recipes/list", func(w http.ResponseWriter, r *http.Request) {
		handleRecipesList(w, r, openQuery)
	})
	mux.HandleFunc("/recipes/get", func(w http.ResponseWriter, r *http.Request) {
		handleRecipesGet(w, r, openQuery)
	})
	mux.HandleFunc("/recipes/asset/ls", func(w http.ResponseWriter, r *http.Request) {
		handleRecipesAssetLs(w, r, openQuery)
	})
	mux.HandleFunc("/recipes/asset/get", func(w http.ResponseWriter, r *http.Request) {
		handleRecipesAssetGet(w, r, openQuery)
	})
}

func handleRecipesList(w http.ResponseWriter, r *http.Request, openQuery func() (*recipes.Query, error)) {
	if r.Method != http.MethodGet {
		http.Error(w, "recipes: GET only", http.StatusMethodNotAllowed)
		return
	}
	q, err := openQuery()
	if err != nil {
		daemonlog.Errorf("recipes/list: open query: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer q.Close()
	items, err := q.List("")
	if err != nil {
		daemonlog.Errorf("recipes/list: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("recipes/list: %d rows", len(items))
	writeJSON(w, items)
}

func handleRecipesGet(w http.ResponseWriter, r *http.Request, openQuery func() (*recipes.Query, error)) {
	if r.Method != http.MethodGet {
		http.Error(w, "recipes: GET only", http.StatusMethodNotAllowed)
		return
	}
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "recipes/get: missing ?name", http.StatusBadRequest)
		return
	}
	q, err := openQuery()
	if err != nil {
		daemonlog.Errorf("recipes/get: open query: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer q.Close()
	rec, err := q.Get(name)
	if errors.Is(err, recipes.ErrRecipeNotFound) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if err != nil {
		daemonlog.Errorf("recipes/get %s: %v", name, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(rec.Content))
	log.Printf("recipes/get: %s (%d bytes)", name, len(rec.Content))
}

func handleRecipesAssetLs(w http.ResponseWriter, r *http.Request, openQuery func() (*recipes.Query, error)) {
	if r.Method != http.MethodGet {
		http.Error(w, "recipes: GET only", http.StatusMethodNotAllowed)
		return
	}
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "recipes/asset/ls: missing ?name", http.StatusBadRequest)
		return
	}
	q, err := openQuery()
	if err != nil {
		daemonlog.Errorf("recipes/asset/ls: open query: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer q.Close()
	list, err := q.ListAssets(r.Context(), name)
	if errors.Is(err, recipes.ErrRecipeNotFound) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if err != nil {
		daemonlog.Errorf("recipes/asset/ls %s: %v", name, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("recipes/asset/ls: %s (%d entries)", name, len(list))
	writeJSON(w, list)
}

func handleRecipesAssetGet(w http.ResponseWriter, r *http.Request, openQuery func() (*recipes.Query, error)) {
	if r.Method != http.MethodGet {
		http.Error(w, "recipes: GET only", http.StatusMethodNotAllowed)
		return
	}
	name := r.URL.Query().Get("name")
	path := r.URL.Query().Get("path")
	if name == "" || path == "" {
		http.Error(w, "recipes/asset/get: missing ?name or ?path", http.StatusBadRequest)
		return
	}
	q, err := openQuery()
	if err != nil {
		daemonlog.Errorf("recipes/asset/get: open query: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer q.Close()
	body, err := q.GetAsset(r.Context(), name, path)
	switch {
	case errors.Is(err, recipes.ErrInvalidAssetPath):
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	case errors.Is(err, recipes.ErrRecipeNotFound), errors.Is(err, recipes.ErrAssetNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	case err != nil:
		daemonlog.Errorf("recipes/asset/get %s/%s: %v", name, path, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentTypeForAsset(path))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
	log.Printf("recipes/asset/get: %s/%s (%d bytes)", name, path, len(body))
}

// contentTypeForAsset returns the HTTP Content-Type for an asset by
// extension. Everything unknown is application/octet-stream — safe for
// piping to a file.
func contentTypeForAsset(p string) string {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".md":
		return "text/markdown; charset=utf-8"
	case ".json":
		return "application/json"
	case ".yaml", ".yml", ".sh", ".txt":
		return "text/plain; charset=utf-8"
	}
	return "application/octet-stream"
}
