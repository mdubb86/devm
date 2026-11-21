package reconcile

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Bucket() falls back to BucketLive for a missing map entry, so a kind
// added without a changeBucket entry would silently route live.
func TestEveryKindHasExplicitBucket(t *testing.T) {
	for _, k := range AllKinds() {
		_, ok := changeBucket[k]
		assert.True(t, ok, "ChangeKind %d has no changeBucket entry", k)
	}
	assert.Len(t, changeBucket, len(AllKinds()), "changeBucket has entries for non-kinds")
}
