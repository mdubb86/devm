package render

// DnsmasqConfig returns the in-VM dnsmasq drop-in config. Written to
// /etc/dnsmasq.d/devm-test.conf inside the VM at boot.
//
// Two directives:
//
//  1. `no-resolv` — do NOT read /etc/resolv.conf for upstream
//     servers. resolv.conf inside the guest points at 127.0.0.1
//     (dnsmasq itself), so without this dnsmasq would query itself
//     and loop.
//
//  2. `server=192.168.127.1` — forward everything to the softnet
//     gateway, which handles policy-appropriate resolution (host
//     resolver in OPEN, iron-proxy's DNS in ENFORCED) and now also
//     answers `.test` itself from its own resolver — loopback for
//     direct services, the hairpin address otherwise. The guest
//     carries no `.test` knowledge of its own. Must stay in sync
//     with softnet.GatewayIP (internal/softnet/config.go).
func DnsmasqConfig() []byte {
	return []byte("no-resolv\nserver=192.168.127.1\n")
}
