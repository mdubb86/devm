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
