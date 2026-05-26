package orchestrator

import (
	"testing"

	"github.com/mtwaage/devm/internal/schema"
	"github.com/stretchr/testify/assert"
)

func cfgWithServices(svcs map[string]schema.Service) schema.Config {
	return schema.Config{Services: svcs}
}

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

func TestComputePortChanges_Add(t *testing.T) {
	old := cfgWithServices(map[string]schema.Service{})
	new := cfgWithServices(map[string]schema.Service{"api": {Canonical: 8080}})
	changes := ComputePortChanges(old, new)
	assert.Len(t, changes, 1)
	assert.Equal(t, KindPortAdd, changes[0].Kind)
	assert.Equal(t, "api", changes[0].Service)
	assert.Equal(t, "8080", changes[0].Key)
	assert.Equal(t, "", changes[0].Old)
	assert.Equal(t, "8080", changes[0].New)
}

func TestComputePortChanges_Remove(t *testing.T) {
	old := cfgWithServices(map[string]schema.Service{"api": {Canonical: 8080}})
	new := cfgWithServices(map[string]schema.Service{})
	changes := ComputePortChanges(old, new)
	assert.Len(t, changes, 1)
	assert.Equal(t, KindPortRemove, changes[0].Kind)
}

func TestComputePortChanges_Change(t *testing.T) {
	old := cfgWithServices(map[string]schema.Service{"api": {Canonical: 8080}})
	new := cfgWithServices(map[string]schema.Service{"api": {Canonical: 9090}})
	changes := ComputePortChanges(old, new)
	assert.Len(t, changes, 1)
	assert.Equal(t, KindPortChange, changes[0].Kind)
	assert.Equal(t, "8080", changes[0].Old)
	assert.Equal(t, "9090", changes[0].New)
}

func TestComputePortChanges_NoOp(t *testing.T) {
	cfg := cfgWithServices(map[string]schema.Service{"api": {Canonical: 8080}})
	assert.Empty(t, ComputePortChanges(cfg, cfg))
}

func TestComputePortChanges_Deterministic(t *testing.T) {
	old := cfgWithServices(map[string]schema.Service{})
	new := cfgWithServices(map[string]schema.Service{
		"zeta":  {Canonical: 3000},
		"alpha": {Canonical: 4000},
	})
	c := ComputePortChanges(old, new)
	assert.Len(t, c, 2)
	assert.Equal(t, "alpha", c[0].Service)
	assert.Equal(t, "zeta", c[1].Service)
}

func TestComputeNetworkChanges_Add(t *testing.T) {
	old := schema.Config{Network: schema.Network{AllowedDomains: []string{"a.com"}}}
	new := schema.Config{Network: schema.Network{AllowedDomains: []string{"a.com", "b.com"}}}
	changes := ComputeNetworkChanges(old, new)
	assert.Len(t, changes, 1)
	assert.Equal(t, KindNetworkAdd, changes[0].Kind)
	assert.Equal(t, "b.com", changes[0].Key)
}

func TestComputeNetworkChanges_Remove(t *testing.T) {
	old := schema.Config{Network: schema.Network{AllowedDomains: []string{"a.com", "b.com"}}}
	new := schema.Config{Network: schema.Network{AllowedDomains: []string{"a.com"}}}
	changes := ComputeNetworkChanges(old, new)
	assert.Len(t, changes, 1)
	assert.Equal(t, KindNetworkRemove, changes[0].Kind)
	assert.Equal(t, "b.com", changes[0].Key)
}

func TestComputeNetworkChanges_Deterministic(t *testing.T) {
	old := schema.Config{}
	new := schema.Config{Network: schema.Network{AllowedDomains: []string{"z.com", "a.com"}}}
	c := ComputeNetworkChanges(old, new)
	assert.Len(t, c, 2)
	assert.Equal(t, "a.com", c[0].Key)
	assert.Equal(t, "z.com", c[1].Key)
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
