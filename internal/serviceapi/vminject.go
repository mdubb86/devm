package serviceapi

import (
	"fmt"
	"strings"
)

// extraMount is a parsed user-declared mount entry.
type extraMount struct {
	hostPath string
	readOnly bool
}

// parseExtraMounts converts CLI-resolved `ABS_HOST_PATH[:ro]` entries into
// hostPath + readOnly pairs. Malformed entries (empty host path) are
// dropped silently — schema.ValidateWithRoot already rejected them
// CLI-side, so this is defense-in-depth.
func parseExtraMounts(entries []string) []extraMount {
	out := make([]extraMount, 0, len(entries))
	for _, e := range entries {
		path, ro := strings.CutSuffix(e, ":ro")
		if path == "" {
			continue
		}
		out = append(out, extraMount{hostPath: path, readOnly: ro})
	}
	return out
}

// buildExtraMountScript mounts one user-declared extra virtiofs share at
// the same absolute path inside the VM as on the host (mirrored). The
// mount tag matches what the /vm/start handler set on the corresponding
// tart.DirMount. Idempotent — safe to re-run on VM restart.
//
// Read-only shares are mounted with `-o ro` and get `ro` in fstab so the
// guest can't accidentally attempt writes that virtiofs would reject.
func buildExtraMountScript(tag, hostPath string, readOnly bool) string {
	fstabOpts := "rw,_netdev"
	mountOpts := ""
	if readOnly {
		fstabOpts = "ro,_netdev"
		mountOpts = "-o ro "
	}
	return fmt.Sprintf(`set -e
sudo mkdir -p %s
mountpoint -q %s || sudo mount %s-t virtiofs %s %s
grep -q '^%s ' /etc/fstab || echo '%s %s virtiofs %s 0 0' | sudo tee -a /etc/fstab
`, hostPath, hostPath, mountOpts, tag, hostPath,
		tag, tag, hostPath, fstabOpts)
}

// buildWorkspaceMountScript mounts the workspace virtiofs share at the same
// absolute path inside the VM as it lives on the host (Ship 4 mirrored-path
// decision). Cirruslabs base image doesn't auto-mount virtiofs shares; without
// this the guest can't see the workspace.
//
// The mount tag is "workspace" — set at `tart run --dir=workspace:...:tag=workspace`
// (see internal/sandbox/tart/tart.go:formatDirArg + serviceapi/vm.go).
// /etc/fstab persists the mount across guest reboots; this script also runs on
// every VM start regardless of whether the mount already came up via fstab, so
// every step here must be idempotent (mount check + fstab grep-guard).
func buildWorkspaceMountScript(workspaceMirrorPath string) string {
	// No chown: Apple Virtualization's virtiofs surfaces the share with the
	// default exec user's ownership already — files authored on the host as
	// uid 501 show up in the guest as uid 1000 (devm). A `chown devm:devm`
	// is a no-op. Pinned by e2e/test_tart_contract_09_*.
	return fmt.Sprintf(`set -e
sudo mkdir -p %s
mountpoint -q %s || sudo mount -t virtiofs workspace %s
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
//     and :443 → MAC_HOST:HTTPSPort. UDP:123 (NTP) also DNATs to
//     MAC_HOST:ntpPort so the daemon's SNTP responder heals guest-clock
//     drift after a Mac sleep. Bypasses traffic already destined for
//     MAC_HOST (so we don't infinite-loop the rewritten packets).
//
//  2. `inet devm_filter`: default-deny OUTPUT chain that only allows
//     post-DNAT traffic to MAC_HOST:{HTTPPort, HTTPSPort} (tcp),
//     MAC_HOST:DNSPort (udp), MAC_HOST:ntpPort (udp), and loopback.
//     Anything else is dropped.
//
// ntpPort=0 skips the NTP DNAT + filter rules — used by unit tests that
// don't spin up an SNTP responder.
func buildNftablesScript(macHost string, httpPort, httpsPort, dnsPort, ntpPort int) string {
	ntpNatRule := ""
	ntpFilterRule := ""
	if ntpPort > 0 {
		// NTP DNAT catches guest→UDP:123 regardless of target IP (timesyncd
		// resolves a pool name via our dnsmasq→iron-proxy path which returns
		// the proxy sentinel, so the destination is never MAC_HOST at match
		// time). DNAT rewrites to MAC_HOST:ntpPort; the reply's SNAT is
		// handled automatically by conntrack.
		ntpNatRule = fmt.Sprintf("    udp dport 123 dnat to %s:%d\n", macHost, ntpPort)
		ntpFilterRule = fmt.Sprintf("    ip daddr %s udp dport %d accept\n", macHost, ntpPort)
	}
	return fmt.Sprintf(`sudo tee /etc/nftables.conf > /dev/null <<EOF
table ip devm_nat {
  chain output {
    type nat hook output priority -100;
    ip daddr %s return
    tcp dport 443 dnat to %s:%d
    tcp dport 80 dnat to %s:%d
%s  }
}

table inet devm_filter {
  chain output {
    type filter hook output priority 0; policy drop;
    ct state established,related accept
    oif lo accept
    ip daddr 127.0.0.0/8 accept
    ip daddr %s tcp dport { %d, %d } accept
    ip daddr %s udp dport %d accept
%s  }
}
EOF
sudo systemctl enable --now nftables
sudo nft -f /etc/nftables.conf
`, macHost, macHost, httpsPort, macHost, httpPort,
		ntpNatRule,
		macHost, httpPort, httpsPort,
		macHost, dnsPort,
		ntpFilterRule)
}

// buildTimesyncdScript configures systemd-timesyncd to sync from
// MAC_HOST. The nftables DNAT catches guest→UDP:123 regardless of
// destination, but pointing timesyncd at MAC_HOST explicitly (rather
// than the default upstream pool) means:
//   - No DNS round-trip on every poll (guest resolves nothing).
//   - PollIntervalMaxSec=64 caps the backoff so a Mac wake heals
//     within ~64 seconds even if the previous poll succeeded.
//   - The unit shows up as "MACH" / MAC_HOST in `timedatectl show-timesync`
//     — operator-obvious that time is coming from the host, not the
//     public internet.
//
// timesyncd is a systemd built-in; no install step needed on Debian.
// `restart` (not `reload`) because timesyncd re-reads its config on
// SIGHUP but not always the drop-in path — a restart is cheap and
// unambiguous.
func buildTimesyncdScript(macHost string) string {
	return fmt.Sprintf(`sudo mkdir -p /etc/systemd/timesyncd.conf.d
sudo tee /etc/systemd/timesyncd.conf.d/devm.conf > /dev/null <<EOF
[Time]
NTP=%s
FallbackNTP=
PollIntervalMinSec=32
PollIntervalMaxSec=64
EOF
sudo systemctl enable --now systemd-timesyncd
sudo systemctl restart systemd-timesyncd
`, macHost)
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
