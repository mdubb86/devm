package config

import (
	"testing"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
)

func TestBindPort(t *testing.T) {
	// Tart VMs have their own IP; canonical port == bind port.
	cfg := schema.Config{Project: schema.Project{ID: "p", SandboxName: "p"}}
	assert.Equal(t, 3000, BindPort(cfg, 3000))
}

func TestHostPort(t *testing.T) {
	// Tart VMs: host port == canonical port on the VM's IP.
	cfg := schema.Config{Project: schema.Project{ID: "p", SandboxName: "p"}}
	assert.Equal(t, 3000, HostPort(cfg, 3000))
}
