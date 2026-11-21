package serviceapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCurrentBundleFingerprint_ReadsFromCacheBuild(t *testing.T) {
	cache := NewStateCache()
	cache.SetBuild(Build{Fingerprint: "abc123"})

	got := CurrentBundleFingerprint(cache)

	assert.Equal(t, "abc123", got)
}

func TestCurrentBundleFingerprint_EmptyWhenBuildUnset(t *testing.T) {
	cache := NewStateCache()

	got := CurrentBundleFingerprint(cache)

	assert.Equal(t, "", got, "no build set → empty fingerprint (never drifted)")
}
