package serviceapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/mdubb86/devm/internal/recipes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildRecipesFixture writes a schema-only recipes DB via the production
// InitSchema, stamps meta.version, inserts a few recipes and their assets,
// and returns the DB path. Tests point openQuery at this path.
func buildRecipesFixture(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "recipes.db")
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer db.Close()

	ctx := context.Background()
	require.NoError(t, recipes.InitSchema(ctx, db))
	_, err = db.ExecContext(ctx,
		`INSERT INTO meta VALUES ('version', 'recipes-v2.0.0')`)
	require.NoError(t, err)

	recipeRows := []struct {
		name, category, display, desc, keywords, content string
	}{
		{"tool/foo", "tool", "Foo", "Foo tool", "foo", "# foo body"},
		{"tool/bar", "tool", "Bar", "Bar tool with zero assets", "bar", "# bar body"},
		{"tool/ai/claude", "ai", "Claude", "Nested-name recipe", "ai claude", "# claude body"},
	}
	for _, r := range recipeRows {
		_, err := db.ExecContext(ctx,
			`INSERT INTO recipes(name, category, display_name, description, keywords, content, since, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, 'recipes-v2.0.0', 1)`,
			r.name, r.category, r.display, r.desc, r.keywords, r.content)
		require.NoError(t, err)
		_, err = db.ExecContext(ctx,
			`INSERT INTO recipes_fts(name, display_name, description, keywords, content)
			 VALUES (?, ?, ?, ?, ?)`,
			r.name, r.display, r.desc, r.keywords, r.content)
		require.NoError(t, err)
	}

	assetRows := []struct {
		recipe, path string
		content      []byte
	}{
		{"tool/foo", "skills/one.md", []byte("# skill one\n")},
		{"tool/foo", "data.json", []byte(`{"x":1}`)},
		{"tool/foo", "data.yaml", []byte("a: 1\n")},
		{"tool/foo", "data.sh", []byte("#!/bin/sh\necho hi\n")},
		{"tool/foo", "blob.bin", []byte{0x00, 0x01, 0x02, 0xff}},
		{"tool/ai/claude", "skills/one.md", []byte("# nested claude skill\n")},
	}
	for _, a := range assetRows {
		_, err := db.ExecContext(ctx,
			`INSERT INTO assets(recipe_name, path, content, size, mode)
			 VALUES (?, ?, ?, ?, ?)`,
			a.recipe, a.path, a.content, int64(len(a.content)), 0o644)
		require.NoError(t, err)
	}

	return dbPath
}

// newRecipesMux wires the four /recipes/* handlers against a fresh mux
// pointed at fixture DB dbPath.
func newRecipesMux(dbPath string) *http.ServeMux {
	mux := http.NewServeMux()
	registerRecipesRoutes(mux, func() (*recipes.Query, error) {
		return recipes.Open(dbPath)
	})
	return mux
}

func doGET(t *testing.T, mux *http.ServeMux, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

func TestRecipesHandler_List_ReturnsMetadata(t *testing.T) {
	mux := newRecipesMux(buildRecipesFixture(t))
	rr := doGET(t, mux, "/recipes/list")

	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.Contains(t, rr.Header().Get("Content-Type"), "application/json")

	var got []recipes.Recipe
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &got))
	require.Len(t, got, 3)
	names := []string{got[0].Name, got[1].Name, got[2].Name}
	assert.Contains(t, names, "tool/foo")
	assert.Contains(t, names, "tool/bar")
	assert.Contains(t, names, "tool/ai/claude")
	for _, r := range got {
		assert.Empty(t, r.Content, "list must not populate content")
	}
}

func TestRecipesHandler_Get_HappyPath(t *testing.T) {
	mux := newRecipesMux(buildRecipesFixture(t))
	rr := doGET(t, mux, "/recipes/get?name=tool/foo")

	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal(t, "text/markdown; charset=utf-8", rr.Header().Get("Content-Type"))
	assert.Equal(t, "# foo body", rr.Body.String())
}

func TestRecipesHandler_Get_MissingReturns404(t *testing.T) {
	mux := newRecipesMux(buildRecipesFixture(t))
	rr := doGET(t, mux, "/recipes/get?name=tool/nope")
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestRecipesHandler_Get_MissingNameReturns400(t *testing.T) {
	mux := newRecipesMux(buildRecipesFixture(t))
	rr := doGET(t, mux, "/recipes/get")
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestRecipesHandler_AssetLs_HappyPath(t *testing.T) {
	mux := newRecipesMux(buildRecipesFixture(t))
	rr := doGET(t, mux, "/recipes/asset/ls?name=tool/foo")

	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.Contains(t, rr.Header().Get("Content-Type"), "application/json")

	var got []recipes.AssetListing
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &got))
	require.Len(t, got, 5)
	// Sorted by path ASC per Query.ListAssets contract.
	assert.Equal(t, "blob.bin", got[0].Path)
	assert.Equal(t, "data.json", got[1].Path)
	assert.Equal(t, "data.sh", got[2].Path)
	assert.Equal(t, "data.yaml", got[3].Path)
	assert.Equal(t, "skills/one.md", got[4].Path)
	assert.Equal(t, int64(4), got[0].Size)
	assert.NotZero(t, got[0].Mode)
}

func TestRecipesHandler_AssetLs_ZeroAssetsReturnsEmptyArray(t *testing.T) {
	mux := newRecipesMux(buildRecipesFixture(t))
	rr := doGET(t, mux, "/recipes/asset/ls?name=tool/bar")

	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	// Empty JSON array, not "null" and not 404.
	body := rr.Body.String()
	assert.Contains(t, body, "[]")
	var got []recipes.AssetListing
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &got))
	assert.Empty(t, got)
}

func TestRecipesHandler_AssetLs_MissingRecipeReturns404(t *testing.T) {
	mux := newRecipesMux(buildRecipesFixture(t))
	rr := doGET(t, mux, "/recipes/asset/ls?name=tool/nope")
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestRecipesHandler_AssetGet_MarkdownContentType(t *testing.T) {
	mux := newRecipesMux(buildRecipesFixture(t))
	rr := doGET(t, mux, "/recipes/asset/get?name=tool/foo&path=skills/one.md")

	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal(t, "text/markdown; charset=utf-8", rr.Header().Get("Content-Type"))
	assert.Equal(t, "# skill one\n", rr.Body.String())
}

func TestRecipesHandler_AssetGet_JSONContentType(t *testing.T) {
	mux := newRecipesMux(buildRecipesFixture(t))
	rr := doGET(t, mux, "/recipes/asset/get?name=tool/foo&path=data.json")

	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))
	assert.Equal(t, `{"x":1}`, rr.Body.String())
}

func TestRecipesHandler_AssetGet_YAMLContentType(t *testing.T) {
	mux := newRecipesMux(buildRecipesFixture(t))
	rr := doGET(t, mux, "/recipes/asset/get?name=tool/foo&path=data.yaml")

	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal(t, "text/plain; charset=utf-8", rr.Header().Get("Content-Type"))
}

func TestRecipesHandler_AssetGet_ShellContentType(t *testing.T) {
	mux := newRecipesMux(buildRecipesFixture(t))
	rr := doGET(t, mux, "/recipes/asset/get?name=tool/foo&path=data.sh")

	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal(t, "text/plain; charset=utf-8", rr.Header().Get("Content-Type"))
}

func TestRecipesHandler_AssetGet_OctetStreamFallback(t *testing.T) {
	mux := newRecipesMux(buildRecipesFixture(t))
	rr := doGET(t, mux, "/recipes/asset/get?name=tool/foo&path=blob.bin")

	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal(t, "application/octet-stream", rr.Header().Get("Content-Type"))
	assert.Equal(t, []byte{0x00, 0x01, 0x02, 0xff}, rr.Body.Bytes())
}

func TestRecipesHandler_AssetGet_InvalidPathReturns400(t *testing.T) {
	mux := newRecipesMux(buildRecipesFixture(t))
	// ../evil trips validAssetPath — both a "non-canonical" and a
	// ".." segment. Query.GetAsset returns ErrInvalidAssetPath before
	// touching the DB.
	rr := doGET(t, mux, "/recipes/asset/get?name=tool/foo&path=../evil")
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestRecipesHandler_AssetGet_MissingRecipeReturns404(t *testing.T) {
	mux := newRecipesMux(buildRecipesFixture(t))
	rr := doGET(t, mux, "/recipes/asset/get?name=tool/nope&path=x.md")
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestRecipesHandler_AssetGet_MissingAssetReturns404(t *testing.T) {
	mux := newRecipesMux(buildRecipesFixture(t))
	rr := doGET(t, mux, "/recipes/asset/get?name=tool/foo&path=missing.md")
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestRecipesHandler_AssetGet_MissingParamsReturn400(t *testing.T) {
	mux := newRecipesMux(buildRecipesFixture(t))
	rr := doGET(t, mux, "/recipes/asset/get?name=tool/foo")
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doGET(t, mux, "/recipes/asset/get?path=x.md")
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestRecipesHandler_EncodedSlashRoundTrip_Get(t *testing.T) {
	mux := newRecipesMux(buildRecipesFixture(t))
	// tool/ai/claude sent as tool%2Fai%2Fclaude. The URL parser must
	// deliver the decoded "tool/ai/claude" name to the handler.
	rr := doGET(t, mux, "/recipes/get?name=tool%2Fai%2Fclaude")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal(t, "# claude body", rr.Body.String())
}

func TestRecipesHandler_EncodedSlashRoundTrip_AssetGet(t *testing.T) {
	mux := newRecipesMux(buildRecipesFixture(t))
	rr := doGET(t, mux, "/recipes/asset/get?name=tool%2Fai%2Fclaude&path=skills%2Fone.md")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal(t, "text/markdown; charset=utf-8", rr.Header().Get("Content-Type"))
	assert.Equal(t, "# nested claude skill\n", rr.Body.String())
}

func TestRecipesHandler_MethodNotAllowedOnEveryEndpoint(t *testing.T) {
	mux := newRecipesMux(buildRecipesFixture(t))
	targets := []string{
		"/recipes/list",
		"/recipes/get?name=tool/foo",
		"/recipes/asset/ls?name=tool/foo",
		"/recipes/asset/get?name=tool/foo&path=skills/one.md",
	}
	for _, target := range targets {
		req := httptest.NewRequest(http.MethodPost, target, nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rr.Code, target)
	}
}

func TestRecipesHandler_OpenQueryFailurePropagates500(t *testing.T) {
	mux := http.NewServeMux()
	sentinel := errors.New("simulated db unavailable")
	registerRecipesRoutes(mux, func() (*recipes.Query, error) {
		return nil, sentinel
	})
	rr := doGET(t, mux, "/recipes/list")
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}
