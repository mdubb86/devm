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

// Host returns the Mac's IP on the vmnet bridge without a VM hint.
// Kept for tests and callers that don't have a VM IP available yet.
// Multi-VM Macs should prefer HostForVM.
func Host() (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("enumerate interfaces: %w", err)
	}
	var allAddrs []net.Addr
	for _, iface := range ifaces {
		if !strings.HasPrefix(iface.Name, "bridge") {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		allAddrs = append(allAddrs, addrs...)
	}
	return pickBridgeIP(allAddrs)
}

// pickBridgeIP picks the first non-loopback IPv4 address. Exposed
// for tests; production callers use Host() or HostForVM().
func pickBridgeIP(addrs []net.Addr) (string, error) {
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil {
			continue
		}
		ip4 := ip.To4()
		if ip4 == nil || ip4.IsLoopback() {
			continue
		}
		return ip4.String(), nil
	}
	return "", fmt.Errorf("no IPv4 address on any bridge* interface — is vmnet up?")
}
