package config

import (
	"testing"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMergeOverridesService(t *testing.T) {
	base := schema.Config{
		Project: schema.Project{Name: "p"},
		Services: schema.OrderedMapOf(map[string]schema.Service{
			"webapp": {Port: 3000, Hostname: "p.test"},
		}),
	}
	host := "custom.test"
	override := schema.ConfigOverride{
		Services: map[string]schema.ServiceOverride{
			"webapp": {Hostname: &host},
		},
	}
	merged, err := Merge(base, override)
	assert.NoError(t, err)
	assert.Equal(t, "custom.test", merged.Services.Entries["webapp"].Hostname)
	assert.Equal(t, 3000, merged.Services.Entries["webapp"].Port, "non-overridden field preserved")
}

func TestMergeServiceEnvPreservesBaseWhenOverrideAbsent(t *testing.T) {
	base := schema.Config{
		Project: schema.Project{Name: "p"},
		Services: schema.OrderedMapOf(map[string]schema.Service{
			"webapp": {Port: 3000, Env: map[string]schema.EnvValue{"LOG_LEVEL": {Literal: "debug"}}},
		}),
	}
	override := schema.ConfigOverride{
		Services: map[string]schema.ServiceOverride{
			"webapp": {}, // no Env — base should pass through
		},
	}
	merged, err := Merge(base, override)
	assert.NoError(t, err)
	assert.Equal(t, "debug", merged.Services.Entries["webapp"].Env["LOG_LEVEL"].Literal)
}

func TestMergeServiceEnvMergesKeys(t *testing.T) {
	base := schema.Config{
		Project: schema.Project{Name: "p"},
		Services: schema.OrderedMapOf(map[string]schema.Service{
			"webapp": {Port: 3000, Env: map[string]schema.EnvValue{"LOG_LEVEL": {Literal: "debug"}}},
		}),
	}
	override := schema.ConfigOverride{
		Services: map[string]schema.ServiceOverride{
			"webapp": {Env: map[string]schema.EnvValue{"API_URL": {Literal: "http://api.local"}}},
		},
	}
	merged, err := Merge(base, override)
	assert.NoError(t, err)
	assert.Equal(t, "debug", merged.Services.Entries["webapp"].Env["LOG_LEVEL"].Literal, "base key preserved")
	assert.Equal(t, "http://api.local", merged.Services.Entries["webapp"].Env["API_URL"].Literal, "override key added")
}

func TestServiceOverrideExecReplacement(t *testing.T) {
	base := schema.Config{
		Services: schema.OrderedMapOf(map[string]schema.Service{
			"redis": {
				ExecArgv: []string{"redis-server", "/etc/redis.conf"},
			},
		}),
	}
	newExec := []string{"redis-server", "--save", ""}
	override := schema.ConfigOverride{
		Services: map[string]schema.ServiceOverride{
			"redis": {
				ExecArgv: &newExec,
			},
		},
	}
	merged, err := Merge(base, override)
	require.NoError(t, err)
	assert.Equal(t, newExec, merged.Services.Entries["redis"].ExecArgv)
}

func TestServiceOverrideExecFuncReplacement(t *testing.T) {
	base := schema.Config{
		Services: schema.OrderedMapOf(map[string]schema.Service{
			"redis": {
				ExecArgv: []string{"redis-server", "/etc/redis.conf"},
			},
		}),
	}
	newFunc := "start-redis"
	override := schema.ConfigOverride{
		Services: map[string]schema.ServiceOverride{
			"redis": {
				ExecFunc: &newFunc,
			},
		},
	}
	merged, err := Merge(base, override)
	require.NoError(t, err)
	assert.Equal(t, "start-redis", merged.Services.Entries["redis"].ExecFunc)
	assert.Nil(t, merged.Services.Entries["redis"].ExecArgv, "ExecFunc override must clear the prior argv form")
}

func TestMerge_OverridesPath(t *testing.T) {
	base := schema.Config{
		Project: schema.Project{Name: "p"},
		Path:    []string{"/r/.cargo/bin"},
	}
	devPath := []string{"/Users/dev/local/bin", "/r/.cargo/bin"}
	override := schema.ConfigOverride{Path: &devPath}
	out, err := Merge(base, override)
	require.NoError(t, err)
	assert.Equal(t, devPath, out.Path,
		"path override should REPLACE the base list entirely")
}

func TestMerge_PreservesPathWhenOverrideNil(t *testing.T) {
	base := schema.Config{
		Project: schema.Project{Name: "p"},
		Path:    []string{"/r/.cargo/bin"},
	}
	override := schema.ConfigOverride{} // no Path override
	out, err := Merge(base, override)
	require.NoError(t, err)
	assert.Equal(t, []string{"/r/.cargo/bin"}, out.Path)
}

func TestMerge_DockerOverride(t *testing.T) {
	base := schema.Config{} // Docker: nil (off)
	override := schema.ConfigOverride{Docker: &schema.DockerConfig{Enabled: true}}

	out, err := Merge(base, override)
	require.NoError(t, err)
	assert.True(t, out.DockerEnabled(), "Docker: want enabled after override")
}

func TestMerge_DockerOverrideAbsentPreserves(t *testing.T) {
	base := schema.Config{Docker: &schema.DockerConfig{Enabled: true}}
	override := schema.ConfigOverride{} // no Docker override

	out, err := Merge(base, override)
	require.NoError(t, err)
	assert.True(t, out.DockerEnabled(), "Docker: want preserved enabled")
}

func TestMerge_DockerOverrideReplacesWholeBlock(t *testing.T) {
	// A non-nil override.Docker replaces the whole block — the base's
	// BuildCache is dropped unless the override repeats it.
	cache := "8G"
	base := schema.Config{Docker: &schema.DockerConfig{Enabled: true, BuildCache: &cache}}
	override := schema.ConfigOverride{Docker: &schema.DockerConfig{Enabled: true}}

	out, err := Merge(base, override)
	require.NoError(t, err)
	require.NotNil(t, out.Docker)
	assert.True(t, out.Docker.Enabled)
	assert.Nil(t, out.Docker.BuildCache, "override is a whole-block replace, not a merge")
}

func TestMerge_SwapOverride(t *testing.T) {
	base := schema.Config{Swap: strPtr("16G")}
	swap := "30%"
	override := schema.ConfigOverride{Swap: &swap}

	out, err := Merge(base, override)
	require.NoError(t, err)
	require.NotNil(t, out.Swap)
	assert.Equal(t, "30%", *out.Swap, "Swap: want 30%% after override")
	assert.NotSame(t, &swap, out.Swap, "Merge copies the value rather than aliasing the override's pointer")
}

func TestMerge_SwapOverrideAbsentPreserves(t *testing.T) {
	base := schema.Config{Swap: strPtr("16G")}
	override := schema.ConfigOverride{} // no Swap override

	out, err := Merge(base, override)
	require.NoError(t, err)
	require.NotNil(t, out.Swap)
	assert.Equal(t, "16G", *out.Swap, "Swap: want preserved 16G")
}

func TestMerge_DiskOverride(t *testing.T) {
	base := schema.Config{Disk: strPtr("32G")}
	disk := "64G"
	override := schema.ConfigOverride{Disk: &disk}

	out, err := Merge(base, override)
	require.NoError(t, err)
	require.NotNil(t, out.Disk)
	assert.Equal(t, "64G", *out.Disk, "Disk: want 64G after override")
	// Merge copies the value rather than aliasing the override's pointer.
	assert.NotSame(t, &disk, out.Disk)
}

func TestMerge_DiskOverrideAbsentPreserves(t *testing.T) {
	base := schema.Config{Disk: strPtr("64G")}
	override := schema.ConfigOverride{} // no Disk override

	out, err := Merge(base, override)
	require.NoError(t, err)
	require.NotNil(t, out.Disk)
	assert.Equal(t, "64G", *out.Disk, "Disk: want preserved 64G")
}

func TestMerge_PreservesReposWhenOverrideAbsent(t *testing.T) {
	base := schema.Config{
		Project: schema.Project{Name: "p"},
		Repos: map[string]schema.RepoConfig{
			"main": {Secret: "github"},
		},
	}
	override := schema.ConfigOverride{} // Repos is project-wide, not overridable
	out, err := Merge(base, override)
	require.NoError(t, err)
	require.Contains(t, out.Repos, "main")
	assert.Equal(t, "github", out.Repos["main"].Secret)
}

func strPtr(s string) *string { return &s }

func TestMerge_CronPerKey(t *testing.T) {
	base := schema.Config{Cron: map[string]string{"a": "0 * * * *", "b": "5 * * * *"}}
	override := schema.ConfigOverride{Cron: map[string]string{"b": "10 * * * *", "c": "0 3 * * *"}}
	merged, err := Merge(base, override)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"a": "0 * * * *", "b": "10 * * * *", "c": "0 3 * * *"}, merged.Cron)
	assert.Equal(t, map[string]string{"a": "0 * * * *", "b": "5 * * * *"}, base.Cron, "base map not mutated")
}

func TestMerge_CronNilBase(t *testing.T) {
	override := schema.ConfigOverride{Cron: map[string]string{"c": "0 3 * * *"}}
	merged, err := Merge(schema.Config{}, override)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"c": "0 3 * * *"}, merged.Cron)
}
