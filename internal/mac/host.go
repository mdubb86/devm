// Package mac discovers Mac-side network info needed by the daemon —
// notably the vmnet bridge IP that VMs use to reach the Mac.
package mac

import (
	"fmt"
	"net"
	"strings"
)

// Host returns the Mac's IP on the vmnet bridge.
//
// macOS Virtualization.framework typically uses 192.168.64.0/24 but
// the actual subnet can vary per-Mac (we've seen 192.168.139.0/24 in
// the wild). Discover by enumerating bridge* interfaces and picking
// the first non-loopback IPv4 address — there's usually only one
// bridge interface on a Mac and it's the vmnet one.
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
// for tests; production callers use Host().
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
