package serviceapi

import "fmt"

// buildEnvScript writes HTTPS_PROXY/HTTP_PROXY/NO_PROXY into
// /etc/environment inside the VM. iron-proxy uses two separate
// ports (HTTP + HTTPS) on the same Mac IP.
func buildEnvScript(macHost string, httpPort, httpsPort int) string {
	return fmt.Sprintf(`sudo tee /etc/environment > /dev/null <<'EOF'
HTTP_PROXY=http://%s:%d
HTTPS_PROXY=http://%s:%d
NO_PROXY=localhost,127.0.0.1,*.test
EOF
`, macHost, httpPort, macHost, httpsPort)
}

// buildNftablesScript installs the default-deny ruleset and enables
// nftables. Only outbound to macHost on the two iron-proxy ports is
// allowed; loopback unrestricted; established connections accepted.
//
// dnsmasq inside the VM still answers DNS on 127.0.0.1:53 (allowed
// via the loopback rule); it forwards external queries via the
// proxy ports (HTTP CONNECT to iron-proxy, not a direct DNS query
// to MAC_HOST:53 — that's why no :53 allow rule).
func buildNftablesScript(macHost string, httpPort, httpsPort int) string {
	return fmt.Sprintf(`sudo tee /etc/nftables.conf > /dev/null <<EOF
table inet devm {
  chain output {
    type filter hook output priority 0; policy drop;
    ct state established,related accept
    oif lo accept
    ip daddr 127.0.0.0/8 accept
    ip daddr %s tcp dport { %d, %d } accept
  }
}
EOF
sudo systemctl enable --now nftables
sudo nft -f /etc/nftables.conf
`, macHost, httpPort, httpsPort)
}

// buildDnsmasqScript adds a forward directive so dnsmasq doesn't
// try to resolve unknown queries against the public internet
// (which nftables blocks). With HTTPS_PROXY set, well-behaved
// clients (curl, npm, pip) send hostnames as part of CONNECT to
// iron-proxy and don't need local DNS resolution.
//
// For clients that DO resolve locally first, dnsmasq's default
// upstream (a public DNS server) is no longer reachable. We don't
// fix that here in v1 — the typical workflow uses HTTP-aware
// tools that go through HTTPS_PROXY.
func buildDnsmasqScript(macHost string) string {
	return fmt.Sprintf(`sudo tee -a /etc/dnsmasq.d/devm.conf > /dev/null <<EOF

# Set by /vm/start: forward unknown queries to MAC_HOST (no effect
# in v1 because nftables blocks direct DNS egress; left for the
# future where iron-proxy serves DNS too).
server=%s
EOF
sudo systemctl reload-or-restart dnsmasq
`, macHost)
}
