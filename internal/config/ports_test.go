package config

import (
	"testing"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
)

func TestBindPort(t *testing.T) {
	cfg := schema.Config{Project: schema.Project{PortOffset: 10}}
	assert.Equal(t, 3010, BindPort(cfg, 3000))
}

func TestHostPort(t *testing.T) {
	cfg := schema.Config{Project: schema.Project{PortOffset: 10}}
	assert.Equal(t, 4010, HostPort(cfg, 3000))
}
