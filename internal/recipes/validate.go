package recipes

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// ErrRecipeNotFound is returned by Query when a recipe with the requested
// name has no row. HTTP handlers dispatch this to 404.
var ErrRecipeNotFound = errors.New("recipe not found")

// ErrAssetNotFound is returned by Query when a recipe exists but has no
// asset at the requested path. HTTP handlers dispatch this to 404.
var ErrAssetNotFound = errors.New("asset not found")

// ErrInvalidAssetPath is returned when an asset path fails ValidAssetPath.
// HTTP handlers dispatch this to 400. Wrapped with %w by ValidAssetPath;
// callers should use errors.Is, not string match.
var ErrInvalidAssetPath = errors.New("invalid asset path")

// ValidAssetPath returns nil if p is a safe relative asset path.
// Rules:
//   - non-empty
//   - no leading "/" and no drive prefix (rejects "/etc/passwd", "C:\...")
//   - no backslashes and no embedded NULs
//   - no ".." segment anywhere; ".", "./", trailing "/" all rejected
//   - already canonical: path.Clean(p) == p (rejects "a//b", "a/./b")
//
// The check is identical at build-time (build-recipes-db) and query-time
// (Query.GetAsset) — one function, both callers.
func ValidAssetPath(p string) error {
	if p == "" {
		return fmt.Errorf("%w: empty", ErrInvalidAssetPath)
	}
	if strings.ContainsAny(p, "\\\x00") {
		return fmt.Errorf("%w: backslash or NUL in %q", ErrInvalidAssetPath, p)
	}
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("%w: absolute path %q", ErrInvalidAssetPath, p)
	}
	// path.Clean returns "." for empty-ish inputs; also strips "./" and
	// collapses "a//b" → "a/b". Comparing pre/post cleanup catches all
	// non-canonical forms in one shot.
	if cleaned := path.Clean(p); cleaned != p {
		return fmt.Errorf("%w: non-canonical %q (clean: %q)", ErrInvalidAssetPath, p, cleaned)
	}
	// After Clean, ".." shows up only at the leading position; explicit
	// check catches "..", "../x", and post-clean surprises.
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("%w: unsafe segment in %q", ErrInvalidAssetPath, p)
		}
	}
	return nil
}
