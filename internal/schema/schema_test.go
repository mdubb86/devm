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
