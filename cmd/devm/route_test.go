package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/mdubb86/devm/internal/softnet"
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

// TestBuildRoutesAllDirectModeVMSkipsVMIP asserts that a direct
// service's route carries no BackendHost even in ModeVM: direct
// services are DNS-only, resolved by the daemon, not dialed via a
// route backend.
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

// TestBuildRoutesModeVMDialsSoftnetLoopback asserts that a proxied
// (non-direct) ModeVM route dials the host-local softnet expose
// listener, not the VM's IP: BackendHost is softnet.HostLoopIP and
// BackendPort is the service's guest port. The project name matches
// no running VM, so if buildRoutes still tried to resolve a VM IP
// this would fail here.
func TestBuildRoutesModeVMDialsSoftnetLoopback(t *testing.T) {
	cfg := schema.Config{
		Project: schema.Project{Name: "proj-no-such-vm"},
		Services: map[string]schema.Service{
			"web": {Port: 8080, Hostname: "web.test"},
		},
	}
	routes, err := buildRoutes(cfg, serviceapi.ModeVM)
	require.NoError(t, err)
	require.Len(t, routes, 1)
	assert.Equal(t, softnet.HostLoopIP, routes[0].BackendHost)
	assert.Equal(t, 8080, routes[0].BackendPort)
}
