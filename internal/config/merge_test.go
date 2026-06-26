package config

import (
	"testing"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMergeOverridesProjectPortOffset(t *testing.T) {
	base := schema.Config{
		Project: schema.Project{ID: "p", SandboxName: "p-sbx", PortOffset: 0},
	}
	off := 50
	override := schema.ConfigOverride{
		Project: &schema.ProjectOverride{PortOffset: &off},
	}
	merged, err := Merge(base, override)
	assert.NoError(t, err)
	assert.Equal(t, 50, merged.Project.PortOffset)
	assert.Equal(t, "p", merged.Project.ID, "non-overridden field preserved")
}

func TestMerge_OverridesProxy(t *testing.T) {
	base := schema.Config{
		Project: schema.Project{ID: "p", SandboxName: "p", Proxy: "caddy"},
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
		Project: schema.Project{ID: "p", SandboxName: "p-sbx"},
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
		Project: schema.Project{ID: "p", SandboxName: "p-sbx"},
		Services: map[string]schema.Service{
			"webapp": {Port: 3000, Env: map[string]string{"LOG_LEVEL": "debug"}},
		},
	}
	override := schema.ConfigOverride{
		Services: map[string]schema.ServiceOverride{
			"webapp": {}, // no Env — base should pass through
		},
	}
	merged, err := Merge(base, override)
	assert.NoError(t, err)
	assert.Equal(t, "debug", merged.Services["webapp"].Env["LOG_LEVEL"])
}

func TestMergeServiceEnvMergesKeys(t *testing.T) {
	base := schema.Config{
		Project: schema.Project{ID: "p", SandboxName: "p-sbx"},
		Services: map[string]schema.Service{
			"webapp": {Port: 3000, Env: map[string]string{"LOG_LEVEL": "debug"}},
		},
	}
	override := schema.ConfigOverride{
		Services: map[string]schema.ServiceOverride{
			"webapp": {Env: map[string]string{"API_URL": "http://api.local"}},
		},
	}
	merged, err := Merge(base, override)
	assert.NoError(t, err)
	assert.Equal(t, "debug", merged.Services["webapp"].Env["LOG_LEVEL"], "base key preserved")
	assert.Equal(t, "http://api.local", merged.Services["webapp"].Env["API_URL"], "override key added")
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

func TestServiceOverrideStartupReplacement(t *testing.T) {
	base := schema.Config{
		Services: map[string]schema.Service{
			"postgres": {
				Port: 5432,
				Startup: []schema.StartupCommand{
					{Command: []string{"old-cmd"}},
				},
			},
		},
	}
	replacement := []schema.StartupCommand{
		{Command: []string{"new-cmd", "--flag"}, Background: true},
	}
	override := schema.ConfigOverride{
		Services: map[string]schema.ServiceOverride{
			"postgres": {
				Startup: &replacement,
			},
		},
	}
	merged, err := Merge(base, override)
	require.NoError(t, err)
	require.Len(t, merged.Services["postgres"].Startup, 1)
	assert.Equal(t, []string{"new-cmd", "--flag"}, merged.Services["postgres"].Startup[0].Command)
	assert.True(t, merged.Services["postgres"].Startup[0].Background)
}

func TestMerge_OverridesPath(t *testing.T) {
	base := schema.Config{
		Project: schema.Project{ID: "p", SandboxName: "p"},
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
		Project: schema.Project{ID: "p", SandboxName: "p"},
		Path:    []string{"/r/.cargo/bin"},
	}
	override := schema.ConfigOverride{} // no Path override
	out, err := Merge(base, override)
	require.NoError(t, err)
	assert.Equal(t, []string{"/r/.cargo/bin"}, out.Path)
}
