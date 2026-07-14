// Package mac discovers Mac-side network info needed by the daemon —
// notably the vmnet bridge IP that VMs use to reach the Mac.
package mac

import (
	"fmt"
	"net"
	"strings"
)

// HostForVM returns the Mac's bridge IP on the same vmnet subnet as vmIP.
//
// Apple Virtualization creates one bridge* interface per VM group; a Mac
// running several VMs concurrently can have bridge100, bridge101,
// bridge102, … each carrying a different 192.168.N.0/24 subnet. iron-proxy
// must bind to the bridge whose subnet contains vmIP — the guest's default
// gateway. Picking the wrong bridge produces silent DNS + egress failure
// (guest routes traffic to a MAC_HOST it cannot reach).
//
// vmIP is the VM's IPv4 address as reported by `tart ip <name>`.
func HostForVM(vmIP string) (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("enumerate interfaces: %w", err)
	}
	var bridgeAddrs []net.Addr
	for _, iface := range ifaces {
		if !strings.HasPrefix(iface.Name, "bridge") {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		bridgeAddrs = append(bridgeAddrs, addrs...)
	}
	return pickBridgeForVM(bridgeAddrs, vmIP)
}

// pickBridgeForVM selects the bridge IP whose subnet contains vmIP.
// Exposed for tests; production callers use HostForVM.
func pickBridgeForVM(bridgeAddrs []net.Addr, vmIP string) (string, error) {
	parsed := net.ParseIP(vmIP).To4()
	if parsed == nil {
		return "", fmt.Errorf("vm ip %q is not an IPv4 address", vmIP)
	}
	for _, a := range bridgeAddrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		if ipn.IP.To4() == nil {
			continue
		}
		if ipn.Contains(parsed) {
			return ipn.IP.To4().String(), nil
		}
	}
	return "", fmt.Errorf("no bridge* interface has a subnet containing vm ip %s", vmIP)
}

