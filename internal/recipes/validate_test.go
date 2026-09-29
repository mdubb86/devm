package recipes

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidAssetPath_Accepts(t *testing.T) {
	for _, p := range []string{
		"a.md",
		"skills/postgres.md",
		"config/nested/deep/file.conf",
		"claude-local-mac.md",
	} {
		assert.NoError(t, ValidAssetPath(p), "should accept: %q", p)
	}
}

func TestValidAssetPath_Rejects(t *testing.T) {
	cases := map[string]string{
		"empty":                         "",
		"absolute":                      "/etc/passwd",
		"dot-dot at start":              "../secret",
		"dot-dot in middle":             "a/../b",
		"dot-dot alone":                 "..",
		"single dot":                    ".",
		"leading dot-slash":             "./a.md",
		"trailing slash":                "a/",
		"leading slash after normalize": "//a",
		"backslash":                     "a\\b",
		"embedded NUL":                  "a\x00b",
		"windows drive":                 `C:\a`,
		"non-canonical":                 "a//b",
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			err := ValidAssetPath(p)
			require.Error(t, err, "should reject: %q", p)
			assert.True(t, errors.Is(err, ErrInvalidAssetPath),
				"error must wrap ErrInvalidAssetPath, got %v", err)
			assert.NotEmpty(t, strings.TrimSpace(err.Error()))
		})
	}
}

func TestErrorSentinels_Distinct(t *testing.T) {
	assert.False(t, errors.Is(ErrRecipeNotFound, ErrAssetNotFound))
	assert.False(t, errors.Is(ErrAssetNotFound, ErrInvalidAssetPath))
	assert.False(t, errors.Is(ErrInvalidAssetPath, ErrRecipeNotFound))
}
