package serviceapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestVMIPForProject(t *testing.T) {
	ironProxyState.put("proj-x", ironProxyInfo{VMIP: "192.168.64.4"})
	t.Cleanup(func() { ironProxyState.del("proj-x") })

	ip, ok := vmIPForProject("proj-x")
	assert.True(t, ok)
	assert.Equal(t, "192.168.64.4", ip)

	_, ok = vmIPForProject("missing")
	assert.False(t, ok)
}

func TestVMIPForProject_StashedWithEmptyVMIP(t *testing.T) {
	ironProxyState.put("proj-y", ironProxyInfo{VMIP: ""})
	t.Cleanup(func() { ironProxyState.del("proj-y") })

	ip, ok := vmIPForProject("proj-y")
	assert.False(t, ok)
	assert.Empty(t, ip)
}
