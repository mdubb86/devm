package render

// DnsmasqConfig returns the in-VM dnsmasq drop-in config: answer
// 127.0.0.1 for any *.test query. Written to
// /etc/dnsmasq.d/devm-test.conf inside the VM at boot.
//
// The `address=/test/127.0.0.1` syntax uses dnsmasq's "complete
// labels" matching — it matches `test`, `app.test`, and any deeper
// subdomain like `foo.bar.test`, but NOT something like `pretest`.
// Verified against the dnsmasq manpage.
func DnsmasqConfig() []byte {
	return []byte("address=/test/127.0.0.1\n")
}
