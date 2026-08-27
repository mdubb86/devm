package mutagen

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEmbeddedSha256_IsNonEmpty(t *testing.T) {
	got := EmbeddedSha256()
	assert.Len(t, got, 64) // hex sha256 = 64 chars
}

func TestEmbeddedVersion_Pinned(t *testing.T) {
	assert.Equal(t, "v0.18.1", EmbeddedVersion())
}
