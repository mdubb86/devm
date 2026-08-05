package render

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	baseimage "github.com/mdubb86/devm/image"
)

func TestDnsmasqConfig_Directives(t *testing.T) {
	out := string(DnsmasqConfig())
	lines := strings.Split(strings.TrimSpace(out), "\n")

	// No .test wildcard: softnet's gateway DNS answers .test (loopback for
	// direct services, the hairpin address otherwise). The guest carries
	// zero .test knowledge.
	assert.NotContains(t, out, "address=")
	assert.Contains(t, lines, "no-resolv")
	assert.Contains(t, lines, "server=192.168.127.1")
	assert.Len(t, lines, 2)
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
