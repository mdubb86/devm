package router

import (
	"context"
	"testing"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cfgWithServices(services map[string]schema.Service, portOffset int) schema.Config {
	return schema.Config{
		Project:  schema.Project{ID: "foo", SandboxName: "foo", PortOffset: portOffset, Proxy: "caddy"},
		Services: services,
	}
}

func TestMappingsFromCfg_EnumeratesServicesWithHostnameAndPort(t *testing.T) {
	cfg := cfgWithServices(map[string]schema.Service{
		"api": {Port: 5000, Hostname: "api.foo.local"},
		"app": {Port: 3000, Hostname: "app.foo.local"},
		"db":  {Port: 5432, Hostname: ""},         // no hostname → skip
		"x":   {Port: 0, Hostname: "x.foo.local"}, // no port → skip
	}, 50000)

	got := mappingsFromCfg(cfg, ModeVM)
	require.Len(t, got, 2)
	for _, m := range got {
		switch m.Hostname {
		case "api.foo.local":
			assert.Equal(t, 55000, m.DialPort)
		case "app.foo.local":
			assert.Equal(t, 53000, m.DialPort)
		default:
			t.Errorf("unexpected hostname: %s", m.Hostname)
		}
	}
}

func TestMappingsFromCfg_LocalMode_UsesCanonicalPort(t *testing.T) {
	cfg := cfgWithServices(map[string]schema.Service{
		"api": {Port: 5000, Hostname: "api.foo.local"},
	}, 50000)

	got := mappingsFromCfg(cfg, ModeLocal)
	require.Len(t, got, 1)
	assert.Equal(t, 5000, got[0].DialPort)
}

func TestApply_ProxyNone_IsNoOp(t *testing.T) {
	cfg := cfgWithServices(map[string]schema.Service{
		"api": {Port: 5000, Hostname: "api.foo.local"},
	}, 50000)
	cfg.Project.Proxy = "none"

	// Use a nil client — if Apply tries to dereference it, we'll see
	// a panic, which proves the proxy-none path took the early exit.
	err := apply(context.Background(), cfg, ModeVM, nil)
	require.NoError(t, err)
}
