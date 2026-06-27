// Package mac discovers Mac-side network info needed by the daemon —
// notably the vmnet bridge IP that VMs use to reach the Mac.
package mac

import (
	"fmt"
	"net"
	"strings"
)

// vmnetSubnet is the default subnet macOS Virtualization.framework
// uses for its shared-bridge networking. May change between Apple
// versions; pickBridgeIP prefers but doesn't strictly require it.
const vmnetSubnet = "192.168.64.0/24"

// Host returns the Mac's IP on the vmnet bridge as a string.
//
// Looks at all bridge* interface addresses; picks the one inside
// vmnet's canonical 192.168.64.0/24 subnet. Returns an error if no
// address in that subnet exists (no VM ever brought up, or vmnet
// moved subnets).
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

// pickBridgeIP picks the first address inside the vmnet subnet.
// Exposed for tests; in production callers use Host().
func pickBridgeIP(addrs []net.Addr) (string, error) {
	_, vmnet, err := net.ParseCIDR(vmnetSubnet)
	if err != nil {
		return "", err
	}
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
		if vmnet.Contains(ip) {
			return ip.String(), nil
		}
	}
	return "", fmt.Errorf("no interface in %s — is a VM running yet?", vmnetSubnet)
}
