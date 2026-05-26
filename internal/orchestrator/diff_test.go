package orchestrator

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBucketStrings(t *testing.T) {
	assert.Equal(t, "live", BucketLive.String())
	assert.Equal(t, "stop+shell", BucketStopShell.String())
	assert.Equal(t, "teardown+shell", BucketTeardownShell.String())
}

func TestChangeKindBuckets(t *testing.T) {
	// Live: ports (add/remove/change) + network add
	assert.Equal(t, BucketLive, KindPortAdd.Bucket())
	assert.Equal(t, BucketLive, KindPortRemove.Bucket())
	assert.Equal(t, BucketLive, KindPortChange.Bucket())
	assert.Equal(t, BucketLive, KindNetworkAdd.Bucket())

	// Stop+shell: network removes, env, startup
	assert.Equal(t, BucketStopShell, KindNetworkRemove.Bucket())
	assert.Equal(t, BucketStopShell, KindEnvAdd.Bucket())
	assert.Equal(t, BucketStopShell, KindEnvRemove.Bucket())
	assert.Equal(t, BucketStopShell, KindEnvChange.Bucket())
	assert.Equal(t, BucketStopShell, KindStartupChange.Bucket())

	// Teardown+shell: install, masks, image, identity
	assert.Equal(t, BucketTeardownShell, KindInstallChange.Bucket())
	assert.Equal(t, BucketTeardownShell, KindMaskChange.Bucket())
	assert.Equal(t, BucketTeardownShell, KindImageChange.Bucket())
	assert.Equal(t, BucketTeardownShell, KindIdentityChange.Bucket())
}

func TestRecreateFlavorPickMax(t *testing.T) {
	// No changes → live only
	assert.Equal(t, FlavorLiveOnly, RecreateFlavor(nil))
	assert.Equal(t, FlavorLiveOnly, RecreateFlavor([]Change{{Kind: KindPortAdd}}))

	// Mix of live + stop → stop wins
	assert.Equal(t, FlavorStopShell, RecreateFlavor([]Change{
		{Kind: KindPortAdd},
		{Kind: KindEnvChange},
	}))

	// Any teardown wins
	assert.Equal(t, FlavorTeardownShell, RecreateFlavor([]Change{
		{Kind: KindPortAdd},
		{Kind: KindEnvChange},
		{Kind: KindInstallChange},
	}))
}
