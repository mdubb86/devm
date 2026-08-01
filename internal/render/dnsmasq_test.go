package render

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	baseimage "github.com/mdubb86/devm/image"
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

// TestDnsmasqConfig_BaseImageParity: the drop-in baked into the base
// image (image/dnsmasq-devm-test.conf) must equal DnsmasqConfig()'s
// output byte-for-byte. Two consumers of the same config file — the
// base image writes it at build time so dnsmasq starts correctly at
// first boot, and install.sh rewrites it on every reconcile via
// render.DnsmasqConfig. If they drift, a reconcile changes the file
// under dnsmasq and reload semantics get confused.
func TestDnsmasqConfig_BaseImageParity(t *testing.T) {
	assert.Equal(t, string(DnsmasqConfig()), baseimage.DnsmasqDevmTestConf,
		"image/dnsmasq-devm-test.conf and render.DnsmasqConfig must match")
}
