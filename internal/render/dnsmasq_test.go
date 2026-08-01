package render

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDnsmasqConfig_Directives(t *testing.T) {
	got := string(DnsmasqConfig())
	assert.True(t, strings.HasSuffix(got, "\n"), "trailing newline")
	lines := strings.Split(strings.TrimSpace(got), "\n")

	// Wildcard *.test → 127.0.0.1 (in-guest caddy target).
	assert.Contains(t, lines, "address=/test/127.0.0.1")
	// Do not read /etc/resolv.conf for upstream — that file points at
	// 127.0.0.1 (this dnsmasq), which would loop.
	assert.Contains(t, lines, "no-resolv")
	// Explicit upstream = softnet gateway. Kept in sync with
	// softnet.GatewayIP (internal/softnet/config.go).
	assert.Contains(t, lines, "server=192.168.127.1")
}
