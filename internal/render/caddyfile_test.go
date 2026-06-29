package render

import (
	"strings"
	"testing"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
)

func TestCaddyfileWithHostnamedServices(t *testing.T) {
	cfg := schema.Config{
		Project: schema.Project{ID: "test", VMName: "test-vm"},
		Services: map[string]schema.Service{
			"webapp": {Port: 3000, Hostname: "test.test"},
			"api":    {Port: 54321, Hostname: "api.test.test"},
			"db":     {Port: 54322}, // no hostname — should be omitted
		},
	}
	out := Caddyfile(cfg)
	// Caddy runs IN the VM and reverse_proxies to the in-VM listen port
	// (svc.Port directly).
	assert.Contains(t, out, "http://test.test")
	assert.Contains(t, out, "reverse_proxy localhost:3000")
	assert.Contains(t, out, "http://api.test.test")
	assert.Contains(t, out, "reverse_proxy localhost:54321")
	// db has no hostname → no block
	assert.False(t, strings.Contains(out, "54322"), "service without hostname must not appear")
	// auto_https off block
	assert.Contains(t, out, "auto_https off")
}
