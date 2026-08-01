package render

// DnsmasqConfig returns the in-VM dnsmasq drop-in config. Written to
// /etc/dnsmasq.d/devm-test.conf inside the VM at boot.
//
// Three directives:
//
//  1. `address=/test/127.0.0.1` — answer 127.0.0.1 for any `*.test`
//     query. dnsmasq's "complete labels" matching covers `test`,
//     `app.test`, and any deeper subdomain like `foo.bar.test`, but
//     NOT something like `pretest`. Verified against the dnsmasq
//     manpage.
//
//  2. `no-resolv` — do NOT read /etc/resolv.conf for upstream
//     servers. resolv.conf inside the guest points at 127.0.0.1
//     (dnsmasq itself), so without this dnsmasq would query itself
//     for non-.test names and loop.
//
//  3. `server=192.168.127.1` — forward everything not-.test to the
//     softnet gateway, which handles policy-appropriate resolution
//     (host resolver in OPEN, iron-proxy's DNS in ENFORCED). Must
//     stay in sync with softnet.GatewayIP (internal/softnet/config.go).
func DnsmasqConfig() []byte {
	return []byte("address=/test/127.0.0.1\nno-resolv\nserver=192.168.127.1\n")
}
