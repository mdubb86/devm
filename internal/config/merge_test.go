package config

import (
	"testing"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMerge_OverridesProxy(t *testing.T) {
	base := schema.Config{
		Project: schema.Project{Name: "p", Proxy: "caddy"},
	}
	proxy := "none"
	override := schema.ConfigOverride{
		Project: &schema.ProjectOverride{Proxy: &proxy},
	}
	out, err := Merge(base, override)
	require.NoError(t, err)
	assert.Equal(t, "none", out.Project.Proxy)
}

func TestMergeOverridesService(t *testing.T) {
	base := schema.Config{
		Project: schema.Project{Name: "p"},
		Services: map[string]schema.Service{
			"webapp": {Port: 3000, Hostname: "p.test"},
		},
	}
	host := "custom.test"
	override := schema.ConfigOverride{
		Services: map[string]schema.ServiceOverride{
			"webapp": {Hostname: &host},
		},
	}
	merged, err := Merge(base, override)
	assert.NoError(t, err)
	assert.Equal(t, "custom.test", merged.Services["webapp"].Hostname)
	assert.Equal(t, 3000, merged.Services["webapp"].Port, "non-overridden field preserved")
}

func TestMergeServiceEnvPreservesBaseWhenOverrideAbsent(t *testing.T) {
	base := schema.Config{
		Project: schema.Project{Name: "p"},
		Services: map[string]schema.Service{
			"webapp": {Port: 3000, Env: map[string]schema.EnvValue{"LOG_LEVEL": {Literal: "debug"}}},
		},
	}
	override := schema.ConfigOverride{
		Services: map[string]schema.ServiceOverride{
			"webapp": {}, // no Env — base should pass through
		},
	}
	merged, err := Merge(base, override)
	assert.NoError(t, err)
	assert.Equal(t, "debug", merged.Services["webapp"].Env["LOG_LEVEL"].Literal)
}

func TestMergeServiceEnvMergesKeys(t *testing.T) {
	base := schema.Config{
		Project: schema.Project{Name: "p"},
		Services: map[string]schema.Service{
			"webapp": {Port: 3000, Env: map[string]schema.EnvValue{"LOG_LEVEL": {Literal: "debug"}}},
		},
	}
	override := schema.ConfigOverride{
		Services: map[string]schema.ServiceOverride{
			"webapp": {Env: map[string]schema.EnvValue{"API_URL": {Literal: "http://api.local"}}},
		},
	}
	merged, err := Merge(base, override)
	assert.NoError(t, err)
	assert.Equal(t, "debug", merged.Services["webapp"].Env["LOG_LEVEL"].Literal, "base key preserved")
	assert.Equal(t, "http://api.local", merged.Services["webapp"].Env["API_URL"].Literal, "override key added")
}

func TestConfigOverrideInstallReplacement(t *testing.T) {
	base := schema.Config{
		Install: []string{"apt-get install -y jq"},
	}
	replacement := []string{"npm install -g typescript"}
	override := schema.ConfigOverride{
		Install: &replacement,
	}
	merged, err := Merge(base, override)
	require.NoError(t, err)
	require.Len(t, merged.Install, 1)
	assert.Equal(t, "npm install -g typescript", merged.Install[0])
}

func TestServiceOverrideExecReplacement(t *testing.T) {
	base := schema.Config{
		Services: map[string]schema.Service{
			"redis": {
				Exec: []string{"redis-server", "/etc/redis.conf"},
			},
		},
	}
	newExec := []string{"redis-server", "--save", ""}
	override := schema.ConfigOverride{
		Services: map[string]schema.ServiceOverride{
			"redis": {
				Exec: &newExec,
			},
		},
	}
	merged, err := Merge(base, override)
	require.NoError(t, err)
	assert.Equal(t, newExec, merged.Services["redis"].Exec)
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
	base := schema.Config{} // Docker: false
	tru := true
	override := schema.ConfigOverride{Docker: &tru}

	out, err := Merge(base, override)
	require.NoError(t, err)
	assert.True(t, out.Docker, "Docker: want true after override")
}

func TestMerge_DockerOverrideAbsentPreserves(t *testing.T) {
	base := schema.Config{Docker: true}
	override := schema.ConfigOverride{} // no Docker override

	out, err := Merge(base, override)
	require.NoError(t, err)
	assert.True(t, out.Docker, "Docker: want preserved true")
}

func TestMerge_DiskOverride(t *testing.T) {
	base := schema.Config{Disk: "32G"}
	disk := "64G"
	override := schema.ConfigOverride{Disk: &disk}

	out, err := Merge(base, override)
	require.NoError(t, err)
	assert.Equal(t, "64G", out.Disk, "Disk: want 64G after override")
}

func TestMerge_DiskOverrideAbsentPreserves(t *testing.T) {
	base := schema.Config{Disk: "64G"}
	override := schema.ConfigOverride{} // no Disk override

	out, err := Merge(base, override)
	require.NoError(t, err)
	assert.Equal(t, "64G", out.Disk, "Disk: want preserved 64G")
}
