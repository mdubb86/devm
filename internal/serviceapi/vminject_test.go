package serviceapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildWorkspaceMountScript_MountsVirtioFSAtMirrorPath(t *testing.T) {
	path := "/Users/michael/workspace/myproject"
	script := buildWorkspaceMountScript(path)
	assert.Contains(t, script, "mount -t virtiofs workspace "+path)
	assert.Contains(t, script, "mkdir -p "+path)
	assert.Contains(t, script, "chown admin:admin "+path)
	assert.Contains(t, script, "workspace "+path+" virtiofs")
}

func TestBuildEnvScript_NoProxyOnly(t *testing.T) {
	script := buildEnvScript()
	assert.Contains(t, script, "NO_PROXY=*")
	// Old HTTPS_PROXY-style assertions removed — the transparent
	// model doesn't use env vars.
	assert.NotContains(t, script, "HTTPS_PROXY=http")
	assert.NotContains(t, script, "HTTP_PROXY=http")
}

func TestBuildNftablesScript_DNATsHTTPAndHTTPS(t *testing.T) {
	script := buildNftablesScript("192.168.64.1", 8080, 8443, 8053)
	// NAT table redirects :443 and :80 to MAC_HOST:proxy_ports.
	assert.Contains(t, script, "table ip devm_nat")
	assert.Contains(t, script, "tcp dport 443 dnat to 192.168.64.1:8443")
	assert.Contains(t, script, "tcp dport 80 dnat to 192.168.64.1:8080")
	// Bypass for traffic already destined for MAC_HOST.
	assert.Contains(t, script, "ip daddr 192.168.64.1 return")
}

func TestBuildNftablesScript_FilterDefaultDenyExceptIronProxyPorts(t *testing.T) {
	script := buildNftablesScript("192.168.64.1", 8080, 8443, 8053)
	assert.Contains(t, script, "table inet devm_filter")
	assert.Contains(t, script, "policy drop;")
	// All three iron-proxy ports allowed to MAC_HOST.
	assert.Contains(t, script, "192.168.64.1")
	assert.Contains(t, script, "8080")
	assert.Contains(t, script, "8443")
	assert.Contains(t, script, "8053")
	assert.Contains(t, script, "systemctl enable --now nftables")
}

func TestBuildDnsmasqScript_ForwardsToIronProxyDNS(t *testing.T) {
	script := buildDnsmasqScript("192.168.64.1", 8053)
	assert.Contains(t, script, "systemctl mask --now systemd-resolved")
	assert.Contains(t, script, "no-resolv")
	assert.Contains(t, script, "server=192.168.64.1#8053")
	assert.Contains(t, script, "address=/test/127.0.0.1")
	assert.Contains(t, script, "nameserver 127.0.0.1")
	assert.Contains(t, script, "systemctl reload-or-restart dnsmasq")
}
