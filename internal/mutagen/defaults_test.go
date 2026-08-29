package mutagen

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDefaultIgnores_ContainsCommonNoise(t *testing.T) {
	got := DefaultIgnores
	// Sanity check: known noise patterns are present.
	assert.Contains(t, got, "**/node_modules/")
	assert.Contains(t, got, "**/.DS_Store")
	assert.Contains(t, got, "*.pyc")
	assert.Contains(t, got, ".git/objects/pack/")
}

func TestDefaultIgnores_NoDuplicates(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range DefaultIgnores {
		assert.False(t, seen[p], "duplicate: %q", p)
		seen[p] = true
	}
}
