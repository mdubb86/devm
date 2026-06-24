package router

import "net"

// CheckResolution returns the subset of hostnames that do NOT
// resolve via the OS resolver chain (/etc/hosts, dnsmasq, mDNS,
// localias, anything else). Mechanism-agnostic by design.
func CheckResolution(hostnames []string) []string {
	var unresolved []string
	for _, h := range hostnames {
		if _, err := net.LookupHost(h); err != nil {
			unresolved = append(unresolved, h)
		}
	}
	return unresolved
}
