package orchestrator

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
	assert.Equal(t, BucketLive, KindNetworkAdd.Bucket())
	assert.Equal(t, BucketLive, KindNetworkRemove.Bucket())
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
	changes, err := ComputeAllChanges(old, new, t.TempDir())
	require.NoError(t, err)
	var kinds []ChangeKind
	for _, c := range changes {
		kinds = append(kinds, c.Kind)
	}
	assert.Contains(t, kinds, KindEnvAdd)
	assert.Contains(t, kinds, KindEnvRemove)
	assert.Contains(t, kinds, KindEnvChange)
}

func TestDiff_ServiceExecChange_IsBucketLive(t *testing.T) {
	old := cfgWithServices(map[string]schema.Service{
		"api": {Exec: []string{"old"}},
	})
	new := cfgWithServices(map[string]schema.Service{
		"api": {Exec: []string{"new"}},
	})
	changes, err := ComputeAllChanges(old, new, t.TempDir())
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
	changes, err := ComputeAllChanges(old, new, t.TempDir())
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
	changes, err := ComputeAllChanges(old, new, t.TempDir())
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
	changes, err := ComputeAllChanges(old, new, t.TempDir())
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
	changes, err := ComputeAllChanges(old, new, t.TempDir())
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
	changes, err := ComputeAllChanges(old, new, t.TempDir())
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
	changes, err := ComputeAllChanges(old, new, t.TempDir())
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
	changes, err := ComputeAllChanges(old, new, t.TempDir())
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
	changes, err := ComputeAllChanges(old, new, t.TempDir())
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
	changes, err := ComputeAllChanges(old, new, t.TempDir())
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
	changes, err := ComputeAllChanges(old, new, t.TempDir())
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
	changes, err := ComputeAllChanges(old, new, t.TempDir())
	require.NoError(t, err)
	assert.Len(t, changes, 1)
	assert.Equal(t, KindMaskAddRemove, changes[0].Kind)
}

func TestComputeImageChange(t *testing.T) {
	// BaseImage is now an empty struct; image changes are detected
	// via identity change or install changes. Test that no KindImageChange
	// is emitted for identical (empty) BaseImage structs.
	old := schema.Config{Project: schema.Project{ID: "p", SandboxName: "p"}}
	new := schema.Config{Project: schema.Project{ID: "p", SandboxName: "p"}}
	changes, err := ComputeAllChanges(old, new, t.TempDir())
	require.NoError(t, err)
	for _, c := range changes {
		assert.NotEqual(t, KindImageChange, c.Kind, "no image change for identical config")
	}
}

func TestComputeIdentityChange(t *testing.T) {
	old := schema.Config{Project: schema.Project{ID: "p1", SandboxName: "s1"}}
	new := schema.Config{Project: schema.Project{ID: "p2", SandboxName: "s1"}}
	changes, err := ComputeAllChanges(old, new, t.TempDir())
	require.NoError(t, err)
	assert.Len(t, changes, 1)
	assert.Equal(t, KindIdentityChange, changes[0].Kind)
}

func TestComputeAllChanges_NoOp(t *testing.T) {
	cfg := schema.Config{
		Project: schema.Project{ID: "p", SandboxName: "p"},
		Services: map[string]schema.Service{
			"api": {Port: 8080, Env: map[string]string{"X": "y"}},
		},
		Network: schema.Network{AllowedDomains: []string{"a.com"}},
		Install: []string{"true"},
	}
	changes, err := ComputeAllChanges(cfg, cfg, t.TempDir())
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
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".devm/templates"), 0o755))

	cfg := schema.Config{
		Project: schema.Project{ID: "p", SandboxName: "p"},
		Services: map[string]schema.Service{
			"a": {Port: 1, Templates: []schema.Template{{Source: "foo.tmpl", Output: "/etc/foo"}}},
		},
	}
	got, err := ComputeTemplateChanges(cfg, dir)
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
		Project: schema.Project{ID: "p", SandboxName: "p"},
		Services: map[string]schema.Service{
			"a": {Port: 1, Templates: []schema.Template{{Source: "foo.tmpl", Output: "/etc/foo"}}},
		},
	}
	// Materialise the installer that WriteDevmDir would have produced.
	require.NoError(t, render.WriteDevmDir(cfg, dir))

	got, err := ComputeTemplateChanges(cfg, dir)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestComputeTemplateChanges_ContentChanged(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "foo.tmpl")
	require.NoError(t, os.WriteFile(src, []byte("v1 {{.Project.ID}}\n"), 0o644))

	cfg := schema.Config{
		Project: schema.Project{ID: "p", SandboxName: "p"},
		Services: map[string]schema.Service{
			"a": {Port: 1, Templates: []schema.Template{{Source: "foo.tmpl", Output: "/etc/foo"}}},
		},
	}
	require.NoError(t, render.WriteDevmDir(cfg, dir)) // baseline on-disk
	// Mutate the source.
	require.NoError(t, os.WriteFile(src, []byte("v2 {{.Project.ID}}\n"), 0o644))

	got, err := ComputeTemplateChanges(cfg, dir)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, KindTemplateChange, got[0].Kind)
	assert.Equal(t, "a", got[0].Service)
	assert.Equal(t, "/etc/foo", got[0].Detail)
}

func TestComputeTemplateChanges_Removed(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "foo.tmpl"), []byte("x"), 0o644))

	cfg1 := schema.Config{
		Project: schema.Project{ID: "p", SandboxName: "p"},
		Services: map[string]schema.Service{
			"a": {Port: 1, Templates: []schema.Template{{Source: "foo.tmpl", Output: "/etc/foo"}}},
		},
	}
	require.NoError(t, render.WriteDevmDir(cfg1, dir))

	// New config drops the template.
	cfg2 := schema.Config{
		Project:  cfg1.Project,
		Services: map[string]schema.Service{"a": {Port: 1}},
	}
	got, err := ComputeTemplateChanges(cfg2, dir)
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
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".devm/templates"), 0o755))

	cfg := schema.Config{
		Project: schema.Project{ID: "p", SandboxName: "p"},
		Services: map[string]schema.Service{
			"a": {Port: 1, Templates: []schema.Template{{Source: "foo.tmpl", Output: "/etc/foo"}}},
		},
	}
	changes, err := ComputeAllChanges(schema.Config{}, cfg, dir)
	require.NoError(t, err)
	found := false
	for _, c := range changes {
		if c.Kind == KindTemplateChange {
			found = true
		}
	}
	assert.True(t, found, "expected KindTemplateChange in ComputeAllChanges output")
}
