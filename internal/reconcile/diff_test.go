package reconcile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mdubb86/devm/internal/render"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cfgWithServices(svcs map[string]schema.Service) schema.Config {
	return schema.Config{Services: svcs}
}

func cfgWith(services map[string]schema.Service) schema.Config {
	return schema.Config{
		Project: schema.Project{
			ID:     "x",
			VMName: "x-vm",
		},
		Services: services,
	}
}

func TestBucketStrings(t *testing.T) {
	assert.Equal(t, "live", BucketLive.String())
	assert.Equal(t, "stop+shell", BucketStopShell.String())
	assert.Equal(t, "teardown+shell", BucketTeardownShell.String())
}

func TestChangeKindBuckets(t *testing.T) {
	// Live: ports (add/remove/change), network (add/remove), env (add/remove/change)
	assert.Equal(t, BucketLive, KindPortAdd.Bucket())
	assert.Equal(t, BucketLive, KindPortRemove.Bucket())
	assert.Equal(t, BucketLive, KindPortChange.Bucket())
	assert.Equal(t, BucketIronProxyRestart, KindNetworkAdd.Bucket())
	assert.Equal(t, BucketIronProxyRestart, KindNetworkRemove.Bucket())
	assert.Equal(t, BucketLive, KindEnvAdd.Bucket())
	assert.Equal(t, BucketLive, KindEnvRemove.Bucket())
	assert.Equal(t, BucketLive, KindEnvChange.Bucket())

	// Teardown+shell: install, packages, masks, mounts, image, identity
	assert.Equal(t, BucketTeardownShell, KindInstallChange.Bucket())
	assert.Equal(t, BucketTeardownShell, KindPackagesChange.Bucket())
	assert.Equal(t, BucketTeardownShell, KindMaskAddRemove.Bucket())
	assert.Equal(t, BucketTeardownShell, KindImageChange.Bucket())
	assert.Equal(t, BucketTeardownShell, KindIdentityChange.Bucket())
	assert.Equal(t, BucketTeardownShell, KindMountAddRemove.Bucket())
	assert.Equal(t, BucketTeardownShell, KindDockerToggle.Bucket())

	// Live: service unit fields and hostname
	assert.Equal(t, BucketLive, KindServiceExecChange.Bucket())
	assert.Equal(t, BucketLive, KindServiceRestartChange.Bucket())
	assert.Equal(t, BucketLive, KindServiceAfterChange.Bucket())
	assert.Equal(t, BucketLive, KindServiceWorkdirChange.Bucket())
	assert.Equal(t, BucketLive, KindServiceUserChange.Bucket())
	assert.Equal(t, BucketLive, KindServiceSystemdOverrideChange.Bucket())
	assert.Equal(t, BucketLive, KindServiceHostnameChange.Bucket())

	// Live: path (same fan-out as env via .devm/.env)
	assert.Equal(t, BucketLive, KindPathChange.Bucket())
}

func TestComputePathChange(t *testing.T) {
	old := cfgWith(map[string]schema.Service{})
	old.Path = []string{"/r/.cargo/bin"}
	new := cfgWith(map[string]schema.Service{})
	new.Path = []string{"/r/.cargo/bin", "/r/node_modules/.bin"}

	changes := computePathChange(old, new)
	require.Len(t, changes, 1)
	assert.Equal(t, KindPathChange, changes[0].Kind)
	assert.Equal(t, "/r/.cargo/bin", changes[0].Old)
	assert.Equal(t, "/r/.cargo/bin:/r/node_modules/.bin", changes[0].New)

	// Same list → no change.
	assert.Empty(t, computePathChange(old, old))

	// Reorder is also a change.
	reordered := cfgWith(map[string]schema.Service{})
	reordered.Path = []string{"/r/node_modules/.bin", "/r/.cargo/bin"}
	assert.Len(t, computePathChange(new, reordered), 1, "reorder must produce a change")
}

func TestComputeMountAddRemove(t *testing.T) {
	old := cfgWith(map[string]schema.Service{})
	old.Mounts = []string{"/etc/hosts:ro"}
	new := cfgWith(map[string]schema.Service{})
	new.Mounts = []string{"/etc/hosts:ro", "/tmp:ro"}

	changes := computeMountAddRemove(old, new)
	require.Len(t, changes, 1)
	assert.Equal(t, KindMountAddRemove, changes[0].Kind)
	assert.Equal(t, BucketTeardownShell, changes[0].Bucket())

	// Same list → no change.
	assert.Empty(t, computeMountAddRemove(old, old))
}

func TestComputePortChanges_Add(t *testing.T) {
	old := cfgWithServices(map[string]schema.Service{})
	new := cfgWithServices(map[string]schema.Service{"api": {Port: 8080}})
	changes := ComputePortChanges(old, new)
	assert.Len(t, changes, 1)
	assert.Equal(t, KindPortAdd, changes[0].Kind)
	assert.Equal(t, "api", changes[0].Service)
	assert.Equal(t, "8080", changes[0].Key)
	assert.Equal(t, "", changes[0].Old)
	assert.Equal(t, "8080", changes[0].New)
}

func TestComputePortChanges_Remove(t *testing.T) {
	old := cfgWithServices(map[string]schema.Service{"api": {Port: 8080}})
	new := cfgWithServices(map[string]schema.Service{})
	changes := ComputePortChanges(old, new)
	assert.Len(t, changes, 1)
	assert.Equal(t, KindPortRemove, changes[0].Kind)
}

func TestComputePortChanges_Change(t *testing.T) {
	old := cfgWithServices(map[string]schema.Service{"api": {Port: 8080}})
	new := cfgWithServices(map[string]schema.Service{"api": {Port: 9090}})
	changes := ComputePortChanges(old, new)
	assert.Len(t, changes, 1)
	assert.Equal(t, KindPortChange, changes[0].Kind)
	assert.Equal(t, "8080", changes[0].Old)
	assert.Equal(t, "9090", changes[0].New)
}

func TestComputePortChanges_NoOp(t *testing.T) {
	cfg := cfgWithServices(map[string]schema.Service{"api": {Port: 8080}})
	assert.Empty(t, ComputePortChanges(cfg, cfg))
}

func TestComputePortChanges_Deterministic(t *testing.T) {
	old := cfgWithServices(map[string]schema.Service{})
	new := cfgWithServices(map[string]schema.Service{
		"zeta":  {Port: 3000},
		"alpha": {Port: 4000},
	})
	c := ComputePortChanges(old, new)
	assert.Len(t, c, 2)
	assert.Equal(t, "alpha", c[0].Service)
	assert.Equal(t, "zeta", c[1].Service)
}

func TestComputeEnvChanges(t *testing.T) {
	old := cfgWithServices(map[string]schema.Service{
		"api": {Env: map[string]schema.EnvValue{"LOG_LEVEL": {Literal: "info"}, "STALE": {Literal: "1"}}},
	})
	new := cfgWithServices(map[string]schema.Service{
		"api": {Env: map[string]schema.EnvValue{"LOG_LEVEL": {Literal: "debug"}, "NEW": {Literal: "yes"}}},
	})
	changes, err := ComputeAllChanges(old, new, t.TempDir(), nil, nil, nil)
	require.NoError(t, err)
	var kinds []ChangeKind
	for _, c := range changes {
		kinds = append(kinds, c.Kind)
	}
	assert.Contains(t, kinds, KindEnvAdd)
	assert.Contains(t, kinds, KindEnvRemove)
	assert.Contains(t, kinds, KindEnvChange)
}

func TestComputeGlobalEnvChanges(t *testing.T) {
	old := schema.Config{Env: map[string]schema.EnvValue{"FOO": {Literal: "old"}, "STALE": {Literal: "1"}}}
	new := schema.Config{Env: map[string]schema.EnvValue{"FOO": {Literal: "new"}, "ADDED": {Literal: "yes"}}}
	changes, err := ComputeAllChanges(old, new, t.TempDir(), nil, nil, nil)
	require.NoError(t, err)
	var kinds []ChangeKind
	for _, c := range changes {
		kinds = append(kinds, c.Kind)
	}
	assert.Contains(t, kinds, KindEnvAdd, "ADDED should surface as an add")
	assert.Contains(t, kinds, KindEnvRemove, "STALE should surface as a remove")
	assert.Contains(t, kinds, KindEnvChange, "FOO should surface as a change")
}

func TestDiff_ServiceExecChange_IsBucketLive(t *testing.T) {
	old := cfgWithServices(map[string]schema.Service{
		"api": {Exec: []string{"old"}},
	})
	new := cfgWithServices(map[string]schema.Service{
		"api": {Exec: []string{"new"}},
	})
	changes, err := ComputeAllChanges(old, new, t.TempDir(), nil, nil, nil)
	require.NoError(t, err)
	found := false
	for _, c := range changes {
		if c.Kind == KindServiceExecChange {
			found = true
			assert.Equal(t, BucketLive, c.Bucket())
			assert.Equal(t, "api", c.Service)
		}
	}
	assert.True(t, found, "expected KindServiceExecChange")
}

func TestDiff_ServiceRestartChange_IsBucketLive(t *testing.T) {
	old := cfgWithServices(map[string]schema.Service{
		"api": {Exec: []string{"run"}, Restart: "no"},
	})
	new := cfgWithServices(map[string]schema.Service{
		"api": {Exec: []string{"run"}, Restart: "always"},
	})
	changes, err := ComputeAllChanges(old, new, t.TempDir(), nil, nil, nil)
	require.NoError(t, err)
	found := false
	for _, c := range changes {
		if c.Kind == KindServiceRestartChange {
			found = true
			assert.Equal(t, BucketLive, c.Bucket())
		}
	}
	assert.True(t, found, "expected KindServiceRestartChange")
}

func TestDiff_ServiceAfterChange_IsBucketLive(t *testing.T) {
	old := cfgWithServices(map[string]schema.Service{
		"api": {Exec: []string{"run"}, After: []string{"network.target"}},
	})
	new := cfgWithServices(map[string]schema.Service{
		"api": {Exec: []string{"run"}, After: []string{"network.target", "db.service"}},
	})
	changes, err := ComputeAllChanges(old, new, t.TempDir(), nil, nil, nil)
	require.NoError(t, err)
	found := false
	for _, c := range changes {
		if c.Kind == KindServiceAfterChange {
			found = true
			assert.Equal(t, BucketLive, c.Bucket())
		}
	}
	assert.True(t, found, "expected KindServiceAfterChange")
}

func TestDiff_ServiceWorkdirChange_IsBucketLive(t *testing.T) {
	old := cfgWithServices(map[string]schema.Service{
		"api": {Exec: []string{"run"}, WorkDir: "/old"},
	})
	new := cfgWithServices(map[string]schema.Service{
		"api": {Exec: []string{"run"}, WorkDir: "/new"},
	})
	changes, err := ComputeAllChanges(old, new, t.TempDir(), nil, nil, nil)
	require.NoError(t, err)
	found := false
	for _, c := range changes {
		if c.Kind == KindServiceWorkdirChange {
			found = true
			assert.Equal(t, BucketLive, c.Bucket())
		}
	}
	assert.True(t, found, "expected KindServiceWorkdirChange")
}

func TestDiff_ServiceUserChange_IsBucketLive(t *testing.T) {
	old := cfgWithServices(map[string]schema.Service{
		"api": {Exec: []string{"run"}, User: "alice"},
	})
	new := cfgWithServices(map[string]schema.Service{
		"api": {Exec: []string{"run"}, User: "bob"},
	})
	changes, err := ComputeAllChanges(old, new, t.TempDir(), nil, nil, nil)
	require.NoError(t, err)
	found := false
	for _, c := range changes {
		if c.Kind == KindServiceUserChange {
			found = true
			assert.Equal(t, BucketLive, c.Bucket())
		}
	}
	assert.True(t, found, "expected KindServiceUserChange")
}

func TestDiff_ServiceSystemdOverrideChange_IsBucketLive(t *testing.T) {
	old := cfgWithServices(map[string]schema.Service{
		"api": {Systemd: "old-unit-content"},
	})
	new := cfgWithServices(map[string]schema.Service{
		"api": {Systemd: "new-unit-content"},
	})
	changes, err := ComputeAllChanges(old, new, t.TempDir(), nil, nil, nil)
	require.NoError(t, err)
	found := false
	for _, c := range changes {
		if c.Kind == KindServiceSystemdOverrideChange {
			found = true
			assert.Equal(t, BucketLive, c.Bucket())
		}
	}
	assert.True(t, found, "expected KindServiceSystemdOverrideChange")
}

func TestDiff_ServiceHostnameChange_IsBucketLive(t *testing.T) {
	old := cfgWithServices(map[string]schema.Service{
		"api": {Port: 8080, Hostname: "api.test"},
	})
	new := cfgWithServices(map[string]schema.Service{
		"api": {Port: 8080, Hostname: "api2.test"},
	})
	changes, err := ComputeAllChanges(old, new, t.TempDir(), nil, nil, nil)
	require.NoError(t, err)
	found := false
	for _, c := range changes {
		if c.Kind == KindServiceHostnameChange {
			found = true
			assert.Equal(t, BucketLive, c.Bucket())
			assert.Equal(t, "api.test", c.Old)
			assert.Equal(t, "api2.test", c.New)
		}
	}
	assert.True(t, found, "expected KindServiceHostnameChange")
}

func TestDiff_PackagesChange_IsBucketTeardownShell(t *testing.T) {
	old := schema.Config{Packages: []string{"jq"}}
	new := schema.Config{Packages: []string{"jq", "ripgrep"}}
	changes, err := ComputeAllChanges(old, new, t.TempDir(), nil, nil, nil)
	require.NoError(t, err)
	found := false
	for _, c := range changes {
		if c.Kind == KindPackagesChange {
			found = true
			assert.Equal(t, BucketTeardownShell, c.Bucket())
		}
	}
	assert.True(t, found, "expected KindPackagesChange")
}

func TestDiff_MaskAddRemove_IsBucketTeardownShell(t *testing.T) {
	old := cfgWithServices(map[string]schema.Service{
		"db": {Masks: []schema.Mask{{Path: "data", Size: "10G"}}},
	})
	new := cfgWithServices(map[string]schema.Service{
		"db": {Masks: []schema.Mask{{Path: "data", Size: "20G"}}},
	})
	changes, err := ComputeAllChanges(old, new, t.TempDir(), nil, nil, nil)
	require.NoError(t, err)
	found := false
	for _, c := range changes {
		if c.Kind == KindMaskAddRemove {
			found = true
			assert.Equal(t, BucketTeardownShell, c.Bucket())
		}
	}
	assert.True(t, found, "expected KindMaskAddRemove")
}

func TestDiff_MountAddRemove_IsBucketTeardownShell(t *testing.T) {
	old := schema.Config{Mounts: []string{"/etc/hosts:ro"}}
	new := schema.Config{Mounts: []string{"/etc/hosts:ro", "/tmp:ro"}}
	changes, err := ComputeAllChanges(old, new, t.TempDir(), nil, nil, nil)
	require.NoError(t, err)
	found := false
	for _, c := range changes {
		if c.Kind == KindMountAddRemove {
			found = true
			assert.Equal(t, BucketTeardownShell, c.Bucket())
		}
	}
	assert.True(t, found, "expected KindMountAddRemove")
}

func TestComputeInstallChanges(t *testing.T) {
	old := schema.Config{Install: []string{"apt-get install -y jq"}}
	new := schema.Config{Install: []string{"apt-get install -y jq curl"}}
	changes, err := ComputeAllChanges(old, new, t.TempDir(), nil, nil, nil)
	require.NoError(t, err)
	assert.Len(t, changes, 1)
	assert.Equal(t, KindInstallChange, changes[0].Kind)
}

func TestComputeMaskAddRemove(t *testing.T) {
	old := cfgWithServices(map[string]schema.Service{
		"db": {Masks: []schema.Mask{{Path: "data", Size: "10G"}}},
	})
	new := cfgWithServices(map[string]schema.Service{
		"db": {Masks: []schema.Mask{{Path: "data", Size: "20G"}}},
	})
	changes, err := ComputeAllChanges(old, new, t.TempDir(), nil, nil, nil)
	require.NoError(t, err)
	assert.Len(t, changes, 1)
	assert.Equal(t, KindMaskAddRemove, changes[0].Kind)
}

func TestComputeImageChange(t *testing.T) {
	// BaseImage is now an empty struct; image changes are detected
	// via identity change or install changes. Test that no KindImageChange
	// is emitted for identical (empty) BaseImage structs.
	old := schema.Config{Project: schema.Project{ID: "p", VMName: "p"}}
	new := schema.Config{Project: schema.Project{ID: "p", VMName: "p"}}
	changes, err := ComputeAllChanges(old, new, t.TempDir(), nil, nil, nil)
	require.NoError(t, err)
	for _, c := range changes {
		assert.NotEqual(t, KindImageChange, c.Kind, "no image change for identical config")
	}
}

func TestComputeIdentityChange(t *testing.T) {
	old := schema.Config{Project: schema.Project{ID: "p1", VMName: "s1"}}
	new := schema.Config{Project: schema.Project{ID: "p2", VMName: "s1"}}
	changes, err := ComputeAllChanges(old, new, t.TempDir(), nil, nil, nil)
	require.NoError(t, err)
	assert.Len(t, changes, 1)
	assert.Equal(t, KindIdentityChange, changes[0].Kind)
}

func TestComputeDockerChange_OnToOff(t *testing.T) {
	old := schema.Config{Docker: true}
	new := schema.Config{Docker: false}
	changes := computeDockerChange(old, new)
	require.Len(t, changes, 1)
	assert.Equal(t, KindDockerToggle, changes[0].Kind)
}

func TestComputeDockerChange_OffToOn(t *testing.T) {
	old := schema.Config{Docker: false}
	new := schema.Config{Docker: true}
	changes := computeDockerChange(old, new)
	require.Len(t, changes, 1)
	assert.Equal(t, KindDockerToggle, changes[0].Kind)
}

func TestComputeDockerChange_NoChange(t *testing.T) {
	old := schema.Config{Docker: true}
	new := schema.Config{Docker: true}
	changes := computeDockerChange(old, new)
	assert.Empty(t, changes)
}

func TestComputeAllChanges_NoOp(t *testing.T) {
	cfg := schema.Config{
		Project: schema.Project{ID: "p", VMName: "p"},
		Services: map[string]schema.Service{
			"api": {Port: 8080, Env: map[string]schema.EnvValue{"X": {Literal: "y"}}},
		},
		Network: schema.Network{Allow: []schema.AllowEntry{{Host: "a.com"}}},
		Install: []string{"true"},
	}
	changes, err := ComputeAllChanges(cfg, cfg, t.TempDir(), nil, nil, nil)
	require.NoError(t, err)
	assert.Empty(t, changes)
}

func TestRecreateFlavorPickMax(t *testing.T) {
	// No changes → live only
	assert.Equal(t, FlavorLiveOnly, RecreateFlavor(nil))
	assert.Equal(t, FlavorLiveOnly, RecreateFlavor([]Change{{Kind: KindPortAdd}}))

	// Any teardown wins
	assert.Equal(t, FlavorTeardownShell, RecreateFlavor([]Change{
		{Kind: KindPortAdd},
		{Kind: KindPackagesChange},
		{Kind: KindInstallChange},
	}))
	// Single teardown change alone also picks teardown.
	assert.Equal(t, FlavorTeardownShell, RecreateFlavor([]Change{
		{Kind: KindInstallChange},
	}))
}

func TestKindTemplateChange_BucketIsLive(t *testing.T) {
	assert.Equal(t, BucketLive, KindTemplateChange.Bucket())
}

func TestComputeTemplateChanges_NewTemplate(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "foo.tmpl"), []byte("x {{.Project.ID}}\n"), 0o644))

	cfg := schema.Config{
		Project: schema.Project{ID: "p", VMName: "p"},
		Services: map[string]schema.Service{
			"a": {Port: 1, Templates: []schema.Template{{Source: "foo.tmpl", Output: "/etc/foo"}}},
		},
	}
	// No last-applied baseline at all → surfaces as new.
	got, err := ComputeTemplateChanges(cfg, dir, nil)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, KindTemplateChange, got[0].Kind)
	assert.Equal(t, "a", got[0].Service)
	assert.Equal(t, "/etc/foo", got[0].Detail)
	assert.Equal(t, "", got[0].Old) // new, not removed
}

func TestComputeTemplateChanges_NoChanges(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "foo.tmpl"), []byte("x {{.Project.ID}}\n"), 0o644))

	cfg := schema.Config{
		Project: schema.Project{ID: "p", VMName: "p"},
		Services: map[string]schema.Service{
			"a": {Port: 1, Templates: []schema.Template{{Source: "foo.tmpl", Output: "/etc/foo"}}},
		},
	}
	// Baseline == what cfg renders right now → no diff.
	baseline, err := render.RenderTemplatesByBasename(cfg, dir)
	require.NoError(t, err)

	got, err := ComputeTemplateChanges(cfg, dir, baseline)
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestComputeTemplateChanges_ContentChanged_SurfacesAsChangeNotAdd pins the
// test_20 regression: the last-applied baseline lives in the daemon
// snapshot (StateSnapshot.TemplateContents), not on the workspace's
// .devm/templates/ — nothing writes there in production. Mutating the
// template *source* after the baseline was captured must surface as a
// "~" change, not a "+" add, even though nothing on disk under
// .devm/templates/ ever existed to compare against.
func TestComputeTemplateChanges_ContentChanged_SurfacesAsChangeNotAdd(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "foo.tmpl")
	require.NoError(t, os.WriteFile(src, []byte("v1 {{.Project.ID}}\n"), 0o644))

	cfg := schema.Config{
		Project: schema.Project{ID: "p", VMName: "p"},
		Services: map[string]schema.Service{
			"a": {Port: 1, Templates: []schema.Template{{Source: "foo.tmpl", Output: "/etc/foo"}}},
		},
	}
	// Capture the baseline the way a snapshot write would (cold-start or
	// post-apply reconcile), THEN mutate the source file — simulating a
	// devm.yaml/template edit between snapshot capture and the next
	// reconcile.
	baseline, err := render.RenderTemplatesByBasename(cfg, dir)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(src, []byte("v2 {{.Project.ID}}\n"), 0o644))

	got, err := ComputeTemplateChanges(cfg, dir, baseline)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, KindTemplateChange, got[0].Kind)
	assert.Equal(t, "a", got[0].Service)
	assert.Equal(t, "/etc/foo", got[0].Detail)
	// Change, not add: Old and New both set → formatChange renders "~",
	// not "+" (see internal/orchestrator/format.go's KindTemplateChange case).
	assert.NotEmpty(t, got[0].Old)
	assert.NotEmpty(t, got[0].New)
}

func TestComputeTemplateChanges_Removed(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "foo.tmpl"), []byte("x"), 0o644))

	cfg1 := schema.Config{
		Project: schema.Project{ID: "p", VMName: "p"},
		Services: map[string]schema.Service{
			"a": {Port: 1, Templates: []schema.Template{{Source: "foo.tmpl", Output: "/etc/foo"}}},
		},
	}
	baseline, err := render.RenderTemplatesByBasename(cfg1, dir)
	require.NoError(t, err)

	// New config drops the template.
	cfg2 := schema.Config{
		Project:  cfg1.Project,
		Services: map[string]schema.Service{"a": {Port: 1}},
	}
	got, err := ComputeTemplateChanges(cfg2, dir, baseline)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, KindTemplateChange, got[0].Kind)
	// Removal: Old set, New empty, Detail names the output.
	assert.NotEmpty(t, got[0].Old)
}

func TestComputeAllChanges_IncludesTemplates(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "foo.tmpl"),
		[]byte("hello {{.Project.ID}}\n"), 0o644))

	cfg := schema.Config{
		Project: schema.Project{ID: "p", VMName: "p"},
		Services: map[string]schema.Service{
			"a": {Port: 1, Templates: []schema.Template{{Source: "foo.tmpl", Output: "/etc/foo"}}},
		},
	}
	changes, err := ComputeAllChanges(schema.Config{}, cfg, dir, nil, nil, nil)
	require.NoError(t, err)
	found := false
	for _, c := range changes {
		if c.Kind == KindTemplateChange {
			found = true
		}
	}
	assert.True(t, found, "expected KindTemplateChange in ComputeAllChanges output")
}

func TestBucketIronProxyRestartString(t *testing.T) {
	assert.Equal(t, "iron-proxy-restart", BucketIronProxyRestart.String())
}

func TestNetworkAndSecretKindsInIronProxyRestartBucket(t *testing.T) {
	assert.Equal(t, BucketIronProxyRestart, KindNetworkAdd.Bucket())
	assert.Equal(t, BucketIronProxyRestart, KindNetworkRemove.Bucket())
	assert.Equal(t, BucketIronProxyRestart, KindSecretAdd.Bucket())
	assert.Equal(t, BucketIronProxyRestart, KindSecretRemove.Bucket())
	assert.Equal(t, BucketIronProxyRestart, KindSecretChange.Bucket())
}

func TestComputeNetworkChanges_AddsAndRemoves(t *testing.T) {
	// Interleaved hosts to verify sort.SliceStable actually reorders results.
	// Raw emission order (adds then removes): aaa.com, zzz2.com, zzz.com, aaa2.com
	// Expected sorted order: aaa.com (Add), aaa2.com (Remove), zzz.com (Remove), zzz2.com (Add)
	old := schema.Config{Network: schema.Network{Allow: []schema.AllowEntry{
		{Host: "zzz.com"},
		{Host: "aaa2.com"},
	}}}
	new := schema.Config{Network: schema.Network{Allow: []schema.AllowEntry{
		{Host: "aaa.com"},
		{Host: "zzz2.com"},
	}}}
	got := computeNetworkChanges(old, new)
	require.Len(t, got, 4)

	// Verify sorted by Key, then by Kind (Add < Remove for same key).
	// Entry 0: aaa.com (Add)
	assert.Equal(t, "aaa.com", got[0].Key)
	assert.Equal(t, KindNetworkAdd, got[0].Kind)
	assert.Equal(t, "aaa.com", got[0].New)

	// Entry 1: aaa2.com (Remove)
	assert.Equal(t, "aaa2.com", got[1].Key)
	assert.Equal(t, KindNetworkRemove, got[1].Kind)
	assert.Equal(t, "aaa2.com", got[1].Old)

	// Entry 2: zzz.com (Remove)
	assert.Equal(t, "zzz.com", got[2].Key)
	assert.Equal(t, KindNetworkRemove, got[2].Kind)
	assert.Equal(t, "zzz.com", got[2].Old)

	// Entry 3: zzz2.com (Add)
	assert.Equal(t, "zzz2.com", got[3].Key)
	assert.Equal(t, KindNetworkAdd, got[3].Kind)
	assert.Equal(t, "zzz2.com", got[3].New)
}

func TestComputeNetworkChanges_NoChange(t *testing.T) {
	cfg := schema.Config{Network: schema.Network{Allow: []schema.AllowEntry{{Host: "a.com"}}}}
	assert.Nil(t, computeNetworkChanges(cfg, cfg))
}

func TestComputeSecretChanges_AllThreeShapes(t *testing.T) {
	old := map[string]string{
		"KEEP_SAME":   "hash_a",
		"WILL_CHANGE": "hash_b_old",
		"WILL_REMOVE": "hash_c",
	}
	new := map[string]string{
		"KEEP_SAME":   "hash_a",
		"WILL_CHANGE": "hash_b_new",
		"WILL_ADD":    "hash_d",
	}
	got := computeSecretChanges(new, old)
	require.Len(t, got, 3)

	assert.Equal(t, KindSecretAdd, got[0].Kind)
	assert.Equal(t, "WILL_ADD", got[0].Key)

	assert.Equal(t, KindSecretChange, got[1].Kind)
	assert.Equal(t, "WILL_CHANGE", got[1].Key)

	assert.Equal(t, KindSecretRemove, got[2].Kind)
	assert.Equal(t, "WILL_REMOVE", got[2].Key)
}

func TestComputeSecretChanges_Empty(t *testing.T) {
	assert.Nil(t, computeSecretChanges(nil, nil))
	assert.Nil(t, computeSecretChanges(map[string]string{"X": "h"}, map[string]string{"X": "h"}))
}

func TestComputeAllChanges_IncludesNetworkAndSecretChanges(t *testing.T) {
	old := schema.Config{
		Project: schema.Project{ID: "p", VMName: "p-vm"},
		Network: schema.Network{Allow: []schema.AllowEntry{{Host: "a.com"}}},
	}
	new := schema.Config{
		Project: schema.Project{ID: "p", VMName: "p-vm"},
		Network: schema.Network{Allow: []schema.AllowEntry{{Host: "a.com"}, {Host: "b.com"}}},
	}
	oldHashes := map[string]string{"TOK": "h_old"}
	newHashes := map[string]string{"TOK": "h_new"}
	changes, err := ComputeAllChanges(old, new, "", nil, oldHashes, newHashes)
	require.NoError(t, err)
	// Exactly one network add + one secret change (both other diffs empty).
	require.Len(t, changes, 2)
	kinds := []ChangeKind{changes[0].Kind, changes[1].Kind}
	assert.Contains(t, kinds, KindNetworkAdd)
	assert.Contains(t, kinds, KindSecretChange)
}
