package schema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestConfigValidatesPortRange(t *testing.T) {
	base := Config{
		Project: Project{ID: "p", SandboxName: "p", HostnameApex: "p.local"},
	}

	// port_offset + canonical exceeds 65535 → error.
	over := base
	over.Project.PortOffset = 70000
	over.Services = map[string]Service{"api": {Canonical: 8080}}
	err := over.Validate()
	require.Error(t, err, "offset+canonical over 65535 must error")
	assert.Contains(t, err.Error(), "65535")

	// canonical out of range (negative / too large) → error.
	bigCanon := base
	bigCanon.Services = map[string]Service{"api": {Canonical: 70000}}
	assert.Error(t, bigCanon.Validate(), "canonical over 65535 must error")

	// Valid combination → no error.
	ok := base
	ok.Project.PortOffset = 51000
	ok.Services = map[string]Service{"api": {Canonical: 8080}} // 59080, fine
	assert.NoError(t, ok.Validate())

	// Exactly at the boundary is allowed.
	boundary := base
	boundary.Project.PortOffset = 60000
	boundary.Services = map[string]Service{"api": {Canonical: 5535}} // 65535
	assert.NoError(t, boundary.Validate())
}

func TestStartupCommandRequiresNonEmptyCommand(t *testing.T) {
	err := StartupCommand{}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "command")

	err = StartupCommand{Command: []string{}}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "command")

	err = StartupCommand{Command: []string{""}}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "command")

	err = StartupCommand{Command: []string{"pg_ctl", "start"}}.Validate()
	assert.NoError(t, err)
}

func TestConfigValidatesInstallSteps(t *testing.T) {
	cfg := Config{
		Project:   Project{ID: "x", SandboxName: "x-sbx", HostnameApex: "x.local"},
		BaseImage: BaseImage{Docker: false},
		Install: []string{
			"", // invalid
		},
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "install[0]")
}

func TestServiceValidatesStartupSteps(t *testing.T) {
	svc := Service{
		Startup: []StartupCommand{
			{Command: []string{}}, // invalid
		},
	}
	err := svc.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "startup[0]")
}

func TestServiceMayHaveOnlyStartup(t *testing.T) {
	// A daemon-style service: no canonical port, no masks, but startup.
	svc := Service{
		Startup: []StartupCommand{
			{Command: []string{"my-daemon"}},
		},
	}
	err := svc.Validate()
	assert.NoError(t, err, "a service with only startup commands should be valid")
}

func TestTemplateValidate(t *testing.T) {
	// Valid.
	ok := Template{Source: "configs/foo.tmpl", Output: "/etc/foo"}
	assert.NoError(t, ok.Validate())

	// Missing source.
	assert.Error(t, Template{Output: "/etc/foo"}.Validate())

	// Missing output.
	assert.Error(t, Template{Source: "foo.tmpl"}.Validate())

	// Path traversal in source — rejected.
	bad := Template{Source: "../etc/passwd", Output: "/etc/foo"}
	err := bad.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path traversal")

	// Cleaned form still escapes — rejected.
	bad2 := Template{Source: "configs/../../etc/passwd", Output: "/etc/foo"}
	assert.Error(t, bad2.Validate())

	// Output must be absolute.
	rel := Template{Source: "foo.tmpl", Output: "etc/foo"}
	err = rel.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absolute")
}
