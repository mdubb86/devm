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

func TestComputeEnvChanges(t *testing.T) {
	old := cfgWithServices(map[string]schema.Service{
		"api": {Env: map[string]string{"LOG_LEVEL": "info", "STALE": "1"}},
	})
	new := cfgWithServices(map[string]schema.Service{
		"api": {Env: map[string]string{"LOG_LEVEL": "debug", "NEW": "yes"}},
	})
	changes := ComputeAllChanges(old, new)
	var kinds []ChangeKind
	for _, c := range changes {
		kinds = append(kinds, c.Kind)
	}
	assert.Contains(t, kinds, KindEnvAdd)
	assert.Contains(t, kinds, KindEnvRemove)
	assert.Contains(t, kinds, KindEnvChange)
}

func TestComputeStartupChanges(t *testing.T) {
	old := cfgWithServices(map[string]schema.Service{
		"api": {Startup: []schema.StartupCommand{{Command: []string{"echo", "a"}}}},
	})
	new := cfgWithServices(map[string]schema.Service{
		"api": {Startup: []schema.StartupCommand{{Command: []string{"echo", "b"}}}},
	})
	changes := ComputeAllChanges(old, new)
	assert.Len(t, changes, 1)
	assert.Equal(t, KindStartupChange, changes[0].Kind)
}

func TestComputeInstallChanges(t *testing.T) {
	old := schema.Config{Install: []string{"apt-get install -y jq"}}
	new := schema.Config{Install: []string{"apt-get install -y jq curl"}}
	changes := ComputeAllChanges(old, new)
	assert.Len(t, changes, 1)
	assert.Equal(t, KindInstallChange, changes[0].Kind)
}

func TestComputeMaskChanges(t *testing.T) {
	old := cfgWithServices(map[string]schema.Service{
		"db": {Masks: []schema.Mask{{Path: "data", Size: "10G"}}},
	})
	new := cfgWithServices(map[string]schema.Service{
		"db": {Masks: []schema.Mask{{Path: "data", Size: "20G"}}},
	})
	changes := ComputeAllChanges(old, new)
	assert.Len(t, changes, 1)
	assert.Equal(t, KindMaskChange, changes[0].Kind)
}

func TestComputeImageChange(t *testing.T) {
	old := schema.Config{BaseImage: schema.BaseImage{Docker: false}}
	new := schema.Config{BaseImage: schema.BaseImage{Docker: true}}
	changes := ComputeAllChanges(old, new)
	assert.Len(t, changes, 1)
	assert.Equal(t, KindImageChange, changes[0].Kind)
}

func TestComputeIdentityChange(t *testing.T) {
	old := schema.Config{Project: schema.Project{ID: "p1", SandboxName: "s1"}}
	new := schema.Config{Project: schema.Project{ID: "p2", SandboxName: "s1"}}
	changes := ComputeAllChanges(old, new)
	assert.Len(t, changes, 1)
	assert.Equal(t, KindIdentityChange, changes[0].Kind)
}

func TestComputeAllChanges_NoOp(t *testing.T) {
	cfg := schema.Config{
		Project: schema.Project{ID: "p", SandboxName: "p", HostnameApex: "p.local"},
		Services: map[string]schema.Service{
			"api": {Canonical: 8080, Env: map[string]string{"X": "y"}},
		},
		Network: schema.Network{AllowedDomains: []string{"a.com"}},
		Install: []string{"true"},
	}
	assert.Empty(t, ComputeAllChanges(cfg, cfg))
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
