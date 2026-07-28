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
// (non-direct) ModeVM route leaves BackendHost unset: the daemon
// substitutes the project's allocated IP at /routes/apply time (see
// internal/serviceapi/routes.go), based on Mode + Direct fields.
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
	assert.Empty(t, routes[0].BackendHost, "vm-mode non-direct routes MUST leave BackendHost unset — daemon substitutes")
	assert.Equal(t, 8080, routes[0].BackendPort)
}

// TestBuildRoutes_VMNonDirect_LeavesBackendHostEmpty asserts that a
// proxied (non-direct) ModeVM route leaves BackendHost as zero-value:
// the daemon substitutes the project's allocated IP at apply time.
func TestBuildRoutes_VMNonDirect_LeavesBackendHostEmpty(t *testing.T) {
	cfg := schema.Config{
		Project: schema.Project{Name: "myproj"},
		Services: map[string]schema.Service{
			"api": {Hostname: "api.test", Port: 8080},
		},
	}
	got, err := buildRoutes(cfg, serviceapi.ModeVM)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "api.test", got[0].Hostname)
	assert.Empty(t, got[0].BackendHost,
		"vm-mode non-direct routes MUST leave BackendHost unset — daemon substitutes")
	assert.Equal(t, serviceapi.ModeVM, got[0].Mode)
}

func TestBuildRoutes_PropagatesExposeHost(t *testing.T) {
	cfg := schema.Config{
		Project: schema.Project{Name: "alpha"},
		Services: map[string]schema.Service{
			"api": {
				Port:       80,
				Hostname:   "api.alpha.test",
				ExposeHost: true,
			},
			"internal": {
				Port:       8080,
				Hostname:   "internal.alpha.test",
				ExposeHost: false,
			},
		},
	}
	routes, err := buildRoutes(cfg, serviceapi.ModeVM)
	require.NoError(t, err)
	byHost := make(map[string]serviceapi.Route)
	for _, r := range routes {
		byHost[r.Hostname] = r
	}
	assert.True(t, byHost["api.alpha.test"].ExposeHost)
	assert.False(t, byHost["internal.alpha.test"].ExposeHost)
}

func TestBuildRoutes_VMDirect_LeavesBackendHostEmpty(t *testing.T) {
	cfg := schema.Config{
		Project: schema.Project{Name: "myproj"},
		Services: map[string]schema.Service{
			"db": {Hostname: "db.test", Port: 5432, Direct: true},
		},
	}
	got, err := buildRoutes(cfg, serviceapi.ModeVM)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.True(t, got[0].Direct)
	assert.Empty(t, got[0].BackendHost,
		"direct routes never carry BackendHost")
}
