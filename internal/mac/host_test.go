package mac

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPickBridgeIP_PicksFirstNonLoopbackIPv4(t *testing.T) {
	addrs := []net.Addr{
		mustCIDR("127.0.0.1/8"),    // loopback, skip
		mustCIDR("192.168.139.3/23"), // pick this
		mustCIDR("169.254.1.1/16"),
	}
	got, err := pickBridgeIP(addrs)
	require.NoError(t, err)
	assert.Equal(t, "192.168.139.3", got)
}

func TestPickBridgeIP_NoAddrs(t *testing.T) {
	_, err := pickBridgeIP(nil)
	require.Error(t, err)
}

func TestPickBridgeForVM_PicksMatchingSubnet(t *testing.T) {
	// Three bridges (one per running VM group). Only bridge102's /24
	// contains the target VM IP — HostForVM must return 192.168.64.1.
	addrs := []net.Addr{
		mustCIDR("192.168.139.3/23"), // bridge100 — wrong subnet
		mustCIDR("192.168.97.1/24"),  // bridge101 — wrong subnet
		mustCIDR("192.168.64.1/24"),  // bridge102 — this one
	}
	got, err := pickBridgeForVM(addrs, "192.168.64.21")
	require.NoError(t, err)
	assert.Equal(t, "192.168.64.1", got)
}

func TestPickBridgeForVM_NoMatch(t *testing.T) {
	addrs := []net.Addr{mustCIDR("192.168.64.1/24")}
	_, err := pickBridgeForVM(addrs, "10.0.0.5")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "10.0.0.5")
}

func TestPickBridgeForVM_InvalidVMIP(t *testing.T) {
	_, err := pickBridgeForVM([]net.Addr{mustCIDR("192.168.64.1/24")}, "not-an-ip")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not an IPv4 address")
}

func mustCIDR(s string) net.Addr {
	ip, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	// net.ParseCIDR masks n.IP to the network address; restore the host IP
	// so the returned *net.IPNet matches what net.Interface.Addrs() returns
	// in production (host address, not the zeroed network address).
	n.IP = ip
	return n
}
