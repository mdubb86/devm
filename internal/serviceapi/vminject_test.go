package serviceapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildWorkspaceMountScript_MountsVirtioFSAtMirrorPath(t *testing.T) {
	path := "/Users/michael/workspace/myproject"
	script := buildWorkspaceMountScript(path)
	assert.Contains(t, script, "mount -t virtiofs workspace "+path)
	assert.Contains(t, script, "mkdir -p "+path)
	assert.Contains(t, script, "workspace "+path+" virtiofs")
	// Explicit: no chown — virtiofs on Apple Virtualization.framework rejects
	// guest-side ownership changes with "Operation not permitted".
	assert.NotContains(t, script, "chown")
}

func TestParseExtraMounts_RWAndRO(t *testing.T) {
	got := parseExtraMounts([]string{
		"/Users/x/data",
		"/Users/x/ro-thing:ro",
		"", // dropped
	})
	require.Len(t, got, 2)
	assert.Equal(t, extraMount{hostPath: "/Users/x/data", readOnly: false}, got[0])
	assert.Equal(t, extraMount{hostPath: "/Users/x/ro-thing", readOnly: true}, got[1])
}

func TestBuildExtraMountScript_RW(t *testing.T) {
	script := buildExtraMountScript("extra_0", "/Users/x/data", false)
	assert.Contains(t, script, "mkdir -p /Users/x/data")
	assert.Contains(t, script, "mount -t virtiofs extra_0 /Users/x/data")
	assert.Contains(t, script, "extra_0 /Users/x/data virtiofs rw,_netdev 0 0")
	// RW must not pass -o ro to mount.
	assert.NotContains(t, script, "-o ro")
}

func TestBuildExtraMountScript_ReadOnly(t *testing.T) {
	script := buildExtraMountScript("extra_1", "/Users/x/ro-thing", true)
	assert.Contains(t, script, "mount -o ro -t virtiofs extra_1 /Users/x/ro-thing")
	assert.Contains(t, script, "extra_1 /Users/x/ro-thing virtiofs ro,_netdev 0 0")
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
