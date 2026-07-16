package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/serviceapi"
)

func TestBuildRoutesEmitsDirect(t *testing.T) {
	cfg := schema.Config{
		Project: schema.Project{Name: "proj"},
		Services: map[string]schema.Service{
			"web": {Port: 8080, Hostname: "web.test"},
			"db":  {Port: 54322, Hostname: "db.test", Direct: true},
		},
	}
	// ModeLocal avoids needing a running VM (no tr.IP call).
	routes, err := buildRoutes(cfg, serviceapi.ModeLocal)
	require.NoError(t, err)

	byHost := map[string]serviceapi.Route{}
	for _, r := range routes {
		byHost[r.Hostname] = r
	}
	assert.False(t, byHost["web.test"].Direct)
	require.True(t, byHost["db.test"].Direct, "direct service must produce a Direct route")
	assert.Equal(t, "proj", byHost["db.test"].Project)
	assert.Empty(t, byHost["db.test"].BackendHost, "direct routes carry no backend")
}

// TestBuildRoutesAllDirectModeVMSkipsVMIP asserts that an all-direct
// project in ModeVM never needs the VM's IP: no route's BackendHost
// depends on it, so buildRoutes must skip the tart.IP call entirely.
// If it didn't, this test would fail here since no VM named
// "proj-all-direct" exists to resolve an IP for.
func TestBuildRoutesAllDirectModeVMSkipsVMIP(t *testing.T) {
	cfg := schema.Config{
		Project: schema.Project{Name: "proj-all-direct"},
		Services: map[string]schema.Service{
			"db": {Port: 54322, Hostname: "db.test", Direct: true},
		},
	}
	routes, err := buildRoutes(cfg, serviceapi.ModeVM)
	require.NoError(t, err)
	require.Len(t, routes, 1)
	assert.True(t, routes[0].Direct)
	assert.Empty(t, routes[0].BackendHost, "direct routes carry no backend")
	assert.Equal(t, serviceapi.ModeVM, routes[0].Mode)
}
