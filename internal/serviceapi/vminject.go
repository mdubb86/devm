package serviceapi

import "fmt"

// buildWorkspaceMountScript mounts the workspace virtiofs share at the same
// absolute path inside the VM as it lives on the host (Ship 4 mirrored-path
// decision). Cirruslabs base image doesn't auto-mount virtiofs shares; without
// this the guest can't see the workspace.
//
// The mount tag is "workspace" — set at `tart run --dir=workspace:...:tag=workspace`
// (see internal/sandbox/tart/tart.go:formatDirArg + serviceapi/vm.go).
// /etc/fstab persists the mount across guest reboots for symmetry with the
// runtime state; devm stops the VM cleanly so a fresh tart run remounts via fstab.
func buildWorkspaceMountScript(workspaceMirrorPath string) string {
	return fmt.Sprintf(`set -e
sudo mkdir -p %s
sudo mount -t virtiofs workspace %s
sudo chown admin:admin %s || true
grep -q '^workspace' /etc/fstab || echo 'workspace %s virtiofs rw,_netdev 0 0' | sudo tee -a /etc/fstab
`, workspaceMirrorPath, workspaceMirrorPath, workspaceMirrorPath, workspaceMirrorPath)
}

// buildEnvScript wipes any HTTPS_PROXY/HTTP_PROXY env that Ship 5
// previously set — the transparent-proxy model doesn't use them.
// /etc/environment becomes a placeholder file with no proxy vars
// (anything else the user had set is preserved by Linux's default
// /etc/environment merging from PAM).
//
// Setting NO_PROXY in case the workload's image happens to have
// HTTPS_PROXY set from a base image we don't control — NO_PROXY=*
// disables it.
func buildEnvScript() string {
	return `sudo tee /etc/environment > /dev/null <<'EOF'
NO_PROXY=*
EOF
`
}

// buildNftablesScript installs two tables:
//
//  1. `ip devm_nat`: NAT chain in OUTPUT hook that rewrites :80 → MAC_HOST:HTTPPort
//     and :443 → MAC_HOST:HTTPSPort. Bypasses traffic already destined for
//     MAC_HOST (so we don't infinite-loop the rewritten packets).
//
//  2. `inet devm_filter`: default-deny OUTPUT chain that only allows
//     post-DNAT traffic to MAC_HOST:{HTTPPort, HTTPSPort, DNSPort} and
//     loopback. Anything else (DNS to public servers, direct IP outbound,
//     non-HTTP TCP) is dropped.
func buildNftablesScript(macHost string, httpPort, httpsPort, dnsPort int) string {
	return fmt.Sprintf(`sudo tee /etc/nftables.conf > /dev/null <<EOF
table ip devm_nat {
  chain output {
    type nat hook output priority -100;
    ip daddr %s return
    tcp dport 443 dnat to %s:%d
    tcp dport 80 dnat to %s:%d
  }
}

table inet devm_filter {
  chain output {
    type filter hook output priority 0; policy drop;
    ct state established,related accept
    oif lo accept
    ip daddr 127.0.0.0/8 accept
    ip daddr %s tcp dport { %d, %d, %d } accept
    ip daddr %s udp dport %d accept
  }
}
EOF
sudo systemctl enable --now nftables
sudo nft -f /etc/nftables.conf
`, macHost, macHost, httpsPort, macHost, httpPort,
		macHost, httpPort, httpsPort, dnsPort,
		macHost, dnsPort)
}

// buildDnsmasqScript points dnsmasq's upstream at iron-proxy's DNS
// server. dnsmasq still answers *.test locally; everything else
// forwards to MAC_HOST:DNSPort. iron-proxy returns its own IP for
// every name, so workload resolutions land at MAC_HOST and get
// DNATed by the nftables rules.
//
// systemd-resolved is masked first because it holds :53 by default
// in the cirruslabs/debian template (binds 127.0.0.53 and 127.0.0.54);
// dnsmasq can't start until resolved is gone. /etc/resolv.conf is
// replaced with a plain "nameserver 127.0.0.1" so tools that respect
// it find dnsmasq.
func buildDnsmasqScript(macHost string, dnsPort int) string {
	return fmt.Sprintf(`sudo systemctl mask --now systemd-resolved.service 2>/dev/null || true
sudo rm -f /etc/resolv.conf
sudo tee /etc/resolv.conf > /dev/null <<'EOF'
nameserver 127.0.0.1
EOF
sudo tee /etc/dnsmasq.d/devm.conf > /dev/null <<EOF
address=/test/127.0.0.1
no-resolv
server=%s#%d
EOF
sudo systemctl reload-or-restart dnsmasq
`, macHost, dnsPort)
}
