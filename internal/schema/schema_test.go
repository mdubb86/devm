package schema

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMaskRequiredFields(t *testing.T) {
	m := Mask{Path: "node_modules", Size: "2G"}
	assert.NoError(t, m.Validate())

	missingPath := Mask{Size: "2G"}
	assert.Error(t, missingPath.Validate())

	missingSize := Mask{Path: "node_modules"}
	assert.Error(t, missingSize.Validate())
}

func TestServiceValidate(t *testing.T) {
	// Minimum valid: just canonical
	s := Service{Canonical: 3000}
	assert.NoError(t, s.Validate())

	// env_host without env_inject is a misconfiguration
	bad := Service{Canonical: 3000, EnvHost: "0.0.0.0"}
	assert.Error(t, bad.Validate(), "env_host requires env_inject=true")

	// env_inject without canonical port has nothing to inject
	noPort := Service{EnvInject: true}
	assert.Error(t, noPort.Validate(), "env_inject requires canonical")

	// Workspace pseudo-service: no port OK, but must have at least one mask
	workspace := Service{Masks: []Mask{{Path: "node_modules", Size: "2G"}}}
	assert.NoError(t, workspace.Validate())

	emptyWorkspace := Service{}
	assert.Error(t, emptyWorkspace.Validate(), "service must have canonical or at least one mask")
}

func TestConfigValidate(t *testing.T) {
	c := Config{
		Project: Project{
			ID:           "test",
			SandboxName:  "test-sbx",
			HostnameApex: "test.local",
		},
		BaseImage: BaseImage{Docker: true},
		Network:   Network{AllowedDomains: []string{"github.com"}},
		Services: map[string]Service{
			"webapp": {Canonical: 3000, Hostname: "test.local"},
		},
	}
	assert.NoError(t, c.Validate())

	// Hostname collision across services
	dup := c
	dup.Services = map[string]Service{
		"webapp": {Canonical: 3000, Hostname: "test.local"},
		"api":    {Canonical: 8080, Hostname: "test.local"},
	}
	assert.Error(t, dup.Validate(), "duplicate hostname")

	// Port collision across services
	dup2 := Config{
		Project:   c.Project,
		BaseImage: c.BaseImage,
		Network:   c.Network,
		Services: map[string]Service{
			"a": {Canonical: 3000},
			"b": {Canonical: 3000},
		},
	}
	assert.Error(t, dup2.Validate(), "duplicate canonical port")

	// Missing required project fields
	bad := c
	bad.Project.ID = ""
	assert.Error(t, bad.Validate(), "project.id required")
}
