package serviceapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildEnvScript_IncludesProxyAndNoProxy(t *testing.T) {
	script := buildEnvScript("192.168.64.1", 8080, 8443)
	assert.Contains(t, script, "HTTP_PROXY=http://192.168.64.1:8080")
	assert.Contains(t, script, "HTTPS_PROXY=http://192.168.64.1:8443")
	assert.Contains(t, script, "NO_PROXY=localhost,127.0.0.1,*.test")
	assert.Contains(t, script, "/etc/environment")
}

func TestBuildNftablesScript_DefaultDenyWithAllowsToMacHost(t *testing.T) {
	script := buildNftablesScript("192.168.64.1", 8080, 8443)
	assert.Contains(t, script, "drop") // default-deny policy
	assert.Contains(t, script, "192.168.64.1")
	// Allows for the two iron-proxy ports.
	assert.Contains(t, script, "8080")
	assert.Contains(t, script, "8443")
	assert.Contains(t, script, "systemctl enable --now nftables")
}

func TestBuildDnsmasqScript_ForwardsToMacHost(t *testing.T) {
	script := buildDnsmasqScript("192.168.64.1")
	assert.Contains(t, script, "server=192.168.64.1")
	assert.Contains(t, script, "/etc/dnsmasq.d/devm.conf")
	assert.Contains(t, script, "systemctl reload-or-restart dnsmasq")
}
