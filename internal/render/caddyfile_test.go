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
			"webapp": {Port: 3000, Hostname: "test.test"},
			"api":    {Port: 54321, Hostname: "api.test.test"},
			"db":     {Port: 54322}, // no hostname — should be omitted
		},
	}
	out := Caddyfile(cfg)
	// Caddy runs IN the VM and reverse_proxies to the in-VM listen
	// port (svc.Port directly), NOT port + port_offset (which is the
	// Mac-side mapping). With port_offset=10 the previous renderer
	// emitted localhost:3010 / 54331, which broke routing because
	// services actually bind at svc.Port.
	assert.Contains(t, out, "http://test.test")
	assert.Contains(t, out, "reverse_proxy localhost:3000")
	assert.Contains(t, out, "http://api.test.test")
	assert.Contains(t, out, "reverse_proxy localhost:54321")
	// db has no hostname → no block
	assert.False(t, strings.Contains(out, "54322"), "service without hostname must not appear")
	// And we must NOT emit the host-side mapped ports.
	assert.NotContains(t, out, "localhost:3010", "Caddyfile must use in-VM listen port, not host bind port")
	assert.NotContains(t, out, "localhost:54331", "Caddyfile must use in-VM listen port, not host bind port")
	// auto_https off block
	assert.Contains(t, out, "auto_https off")
}

// TestCaddyfileIgnoresPortOffset is a focused regression test for the
// 2026-06-12 bug where reverse_proxy targets used port + port_offset
// (host bind) instead of svc.Port (in-VM listen). The bug surfaced
// only when port_offset != 0; with offset 0, the rendered Caddyfile
// was accidentally correct.
func TestCaddyfileIgnoresPortOffset(t *testing.T) {
	cfg := schema.Config{
		Project: schema.Project{PortOffset: 100},
		Services: map[string]schema.Service{
			"web": {Port: 8000, Hostname: "x.test"},
		},
	}
	out := Caddyfile(cfg)
	assert.Contains(t, out, "reverse_proxy localhost:8000",
		"reverse_proxy must target the in-VM listen port (8000)")
	assert.NotContains(t, out, "localhost:8100",
		"reverse_proxy must NOT target the host bind port (8000+100)")
}
