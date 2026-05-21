package config

import (
	"testing"

	"github.com/mtwaage/devm/internal/schema"
	"github.com/stretchr/testify/assert"
)

func TestMergeOverridesProjectPortOffset(t *testing.T) {
	base := schema.Config{
		Project: schema.Project{ID: "p", SandboxName: "p-sbx", HostnameApex: "p.local", PortOffset: 0},
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

func TestMergeOverridesService(t *testing.T) {
	base := schema.Config{
		Project: schema.Project{ID: "p", SandboxName: "p-sbx", HostnameApex: "p.local"},
		Services: map[string]schema.Service{
			"webapp": {Canonical: 3000, Hostname: "p.local"},
		},
	}
	host := "custom.local"
	override := schema.ConfigOverride{
		Services: map[string]schema.ServiceOverride{
			"webapp": {Hostname: &host},
		},
	}
	merged, err := Merge(base, override)
	assert.NoError(t, err)
	assert.Equal(t, "custom.local", merged.Services["webapp"].Hostname)
	assert.Equal(t, 3000, merged.Services["webapp"].Canonical, "non-overridden field preserved")
}
