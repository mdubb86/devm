package render

import (
	"strings"
	"testing"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
)

func TestHostsFragment_EmptyWhenNoHostnames(t *testing.T) {
	cfg := schema.Config{
		Services: map[string]schema.Service{
			"db": {Port: 5432}, // no hostname
		},
	}
	assert.Empty(t, HostsFragment(cfg))
}

func TestHostsFragment_OneLinePerHostnamedService(t *testing.T) {
	cfg := schema.Config{
		Services: map[string]schema.Service{
			"web": {Port: 80, Hostname: "web.x.local"},
			"api": {Port: 8080, Hostname: "api.x.local"},
			"db":  {Port: 5432}, // no hostname → omitted
		},
	}
	got := HostsFragment(cfg)
	assert.Contains(t, got, "127.0.0.1 web.x.local\n")
	assert.Contains(t, got, "127.0.0.1 api.x.local\n")
	assert.NotContains(t, got, "db", "service without hostname must not appear")
}

func TestHostsFragment_SortedDeterministically(t *testing.T) {
	cfg := schema.Config{
		Services: map[string]schema.Service{
			"z": {Port: 1, Hostname: "z.local"},
			"a": {Port: 2, Hostname: "a.local"},
			"m": {Port: 3, Hostname: "m.local"},
		},
	}
	got := HostsFragment(cfg)
	aIdx := strings.Index(got, "a.local")
	mIdx := strings.Index(got, "m.local")
	zIdx := strings.Index(got, "z.local")
	assert.Less(t, aIdx, mIdx, "alphabetical: a before m")
	assert.Less(t, mIdx, zIdx, "alphabetical: m before z")
}

func TestHostsFragment_PointsAtLoopback(t *testing.T) {
	// Sanity: every entry must be 127.0.0.1 since Caddy lives in the
	// VM on loopback. NOT 0.0.0.0 (that would be bind-all, wrong for
	// /etc/hosts which expects a destination IP).
	cfg := schema.Config{
		Services: map[string]schema.Service{
			"web": {Port: 80, Hostname: "web.x.local"},
		},
	}
	got := HostsFragment(cfg)
	for _, line := range strings.Split(strings.TrimSpace(got), "\n") {
		assert.True(t, strings.HasPrefix(line, "127.0.0.1 "),
			"entry must start with 127.0.0.1: %q", line)
	}
}
