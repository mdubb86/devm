package render

import (
	"strings"
	"testing"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
)

func TestCaddyfileWithHostnamedServices(t *testing.T) {
	cfg := schema.Config{
		Project: schema.Project{PortOffset: 10},
		Services: map[string]schema.Service{
			"webapp": {Port: 3000, Hostname: "test.local"},
			"api":    {Port: 54321, Hostname: "api.test.local"},
			"db":     {Port: 54322}, // no hostname — should be omitted
		},
	}
	out := Caddyfile(cfg)
	// Bind port = canonical + offset
	assert.Contains(t, out, "http://test.local")
	assert.Contains(t, out, "reverse_proxy localhost:3010")
	assert.Contains(t, out, "http://api.test.local")
	assert.Contains(t, out, "reverse_proxy localhost:54331")
	// db has no hostname → no block
	assert.False(t, strings.Contains(out, "54332"), "service without hostname must not appear")
	// auto_https off block
	assert.Contains(t, out, "auto_https off")
}
