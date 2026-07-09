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
//  1. `ip devm_nat`:
//     - `output` chain (guest-originated): rewrites :80 → MAC_HOST:HTTPPort,
//       :443 → MAC_HOST:HTTPSPort, and UDP:123 → MAC_HOST:ntpPort (SNTP
//       heal for post-Mac-sleep clock drift). Skips traffic already
//       destined for MAC_HOST to avoid infinite loops.
//     - `prerouting` chain (container-originated, i.e. packets arriving on
//       a docker bridge): same HTTP/HTTPS DNAT to iron-proxy PLUS a
//       `redirect` for UDP:53 to the guest's dnsmasq on port 53. Scoped
//       by iifname pattern (`docker*`, `br-*`) so packets arriving from
//       the Mac (eth0) aren't affected. This is what closes the
//       container→internet bypass — a container asking for DNS or HTTP
//       gets funneled through the same iron-proxy path the guest uses,
//       transparently, without any /etc/docker/daemon.json rewiring.
//
//  2. `inet devm_filter`:
//     - `output` chain (default-deny): allows loopback, 127.0.0.0/8, and
//       post-DNAT traffic to MAC_HOST at iron-proxy's HTTP/HTTPS/DNS/NTP
//       ports. Everything else drops. `jump user_output` at the tail
//       gives recipes an escape hatch.
//     - `forward` chain (default-deny for container egress): allows
//       return traffic (`ct state established,related`) and post-DNAT'd
//       container HTTP/HTTPS heading to MAC_HOST:iron-proxy. Everything
//       else drops. `jump user_forward` at the tail for the same
//       escape-hatch pattern. Container→random-port:N (SSH, custom
//       APIs) hits the drop — same allow-list model the guest gets.
//
// Recipe rules survive across VM reboot because we snapshot each user
// chain to /etc/nftables.d/*.conf and /etc/nftables.conf ends with
// `include` glob. systemd's nftables.service re-runs `nft -f
// /etc/nftables.conf` on every boot; the include restores whatever the
// recipe added.
//
// Live-apply uses idempotent `add table` / `add chain` primitives + a
// scoped `flush chain <our-chain>` so the recipe scaffold + any rules
// recipes have added aren't wiped when enforcement fires.
//
// ntpPort=0 skips the NTP DNAT + filter rules — used by unit tests that
// don't spin up an SNTP responder.
func buildNftablesScript(macHost string, httpPort, httpsPort, dnsPort, ntpPort int) string {
	ntpNatRule := ""
	ntpFilterRule := ""
	if ntpPort > 0 {
		// NTP DNAT catches guest→UDP:123 regardless of target IP —
		// timesyncd is pointed at the proxy sentinel (see
		// buildTimesyncdScript), so packets go through the DNAT rather
		// than falling into the `ip daddr MAC_HOST return` bypass.
		// DNAT rewrites to MAC_HOST:ntpPort; the reply's SNAT is
		// handled automatically by conntrack.
		ntpNatRule = fmt.Sprintf("    udp dport 123 dnat to %s:%d\n", macHost, ntpPort)
		ntpFilterRule = fmt.Sprintf("    ip daddr %s udp dport %d accept\n", macHost, ntpPort)
	}
	// Two-stage live apply: first idempotently ensure tables/chains
	// exist (preserves user_output/user_forward if the scaffold step
	// created them + any rules recipes have added), then flush ONLY
	// our own chains (`output`, `forward`, `prerouting`) and rebuild.
	// The user chains are never flushed by us.
	liveApply := fmt.Sprintf(`sudo nft -f - <<'EOF'
add table ip devm_nat
add chain ip devm_nat output { type nat hook output priority -100 ; }
add chain ip devm_nat prerouting { type nat hook prerouting priority -100 ; }
flush chain ip devm_nat output
flush chain ip devm_nat prerouting
add rule ip devm_nat output ip daddr %s return
add rule ip devm_nat output tcp dport 443 dnat to %s:%d
add rule ip devm_nat output tcp dport 80 dnat to %s:%d
%s
add rule ip devm_nat prerouting iifname { "docker0", "docker_gwbridge" } udp dport 53 redirect to :53
add rule ip devm_nat prerouting iifname { "docker0", "docker_gwbridge" } tcp dport 443 dnat to %s:%d
add rule ip devm_nat prerouting iifname { "docker0", "docker_gwbridge" } tcp dport 80 dnat to %s:%d
add rule ip devm_nat prerouting iifname "br-*" udp dport 53 redirect to :53
add rule ip devm_nat prerouting iifname "br-*" tcp dport 443 dnat to %s:%d
add rule ip devm_nat prerouting iifname "br-*" tcp dport 80 dnat to %s:%d

add table inet devm_filter
add chain inet devm_filter user_output
add chain inet devm_filter user_forward
add chain inet devm_filter output { type filter hook output priority 0 ; policy drop ; }
add chain inet devm_filter forward { type filter hook forward priority 0 ; policy drop ; }
flush chain inet devm_filter output
flush chain inet devm_filter forward
add rule inet devm_filter output ct state established,related accept
add rule inet devm_filter output oif lo accept
add rule inet devm_filter output ip daddr 127.0.0.0/8 accept
add rule inet devm_filter output ip daddr %s tcp dport { %d, %d } accept
add rule inet devm_filter output ip daddr %s udp dport %d accept
%s
add rule inet devm_filter output jump user_output
add rule inet devm_filter forward ct state established,related accept
add rule inet devm_filter forward ip daddr %s tcp dport { %d, %d } accept
add rule inet devm_filter forward jump user_forward
EOF
`, macHost, macHost, httpsPort, macHost, httpPort,
		strings.TrimRight(strings.Replace(ntpNatRule, "    ", "add rule ip devm_nat output ", 1), "\n"),
		macHost, httpsPort, macHost, httpPort,
		macHost, httpsPort, macHost, httpPort,
		macHost, httpPort, httpsPort,
		macHost, dnsPort,
		strings.TrimRight(strings.Replace(ntpFilterRule, "    ", "add rule inet devm_filter output ", 1), "\n"),
		macHost, httpPort, httpsPort)

	// Persistence: snapshot user chains and write /etc/nftables.conf
	// so systemd's nftables.service restores everything on the next
	// boot. The include glob catches whatever /etc/nftables.d/*.conf
	// contains — nftables merges re-declared table blocks so chain
	// rules append.
	persist := fmt.Sprintf(`sudo mkdir -p /etc/nftables.d
sudo sh -c 'nft list chain inet devm_filter user_output > /etc/nftables.d/user_output.conf'
sudo sh -c 'nft list chain inet devm_filter user_forward > /etc/nftables.d/user_forward.conf'
sudo tee /etc/nftables.conf > /dev/null <<'EOF'
#!/usr/sbin/nft -f
flush ruleset

table ip devm_nat {
  chain output {
    type nat hook output priority -100;
    ip daddr %s return
    tcp dport 443 dnat to %s:%d
    tcp dport 80 dnat to %s:%d
%s  }
  chain prerouting {
    type nat hook prerouting priority -100;
    iifname { "docker0", "docker_gwbridge" } udp dport 53 redirect to :53
    iifname { "docker0", "docker_gwbridge" } tcp dport 443 dnat to %s:%d
    iifname { "docker0", "docker_gwbridge" } tcp dport 80 dnat to %s:%d
    iifname "br-*" udp dport 53 redirect to :53
    iifname "br-*" tcp dport 443 dnat to %s:%d
    iifname "br-*" tcp dport 80 dnat to %s:%d
  }
}

table inet devm_filter {
  chain user_output {}
  chain user_forward {}
  chain output {
    type filter hook output priority 0; policy drop;
    ct state established,related accept
    oif lo accept
    ip daddr 127.0.0.0/8 accept
    ip daddr %s tcp dport { %d, %d } accept
    ip daddr %s udp dport %d accept
%s    jump user_output
  }
  chain forward {
    type filter hook forward priority 0; policy drop;
    ct state established,related accept
    ip daddr %s tcp dport { %d, %d } accept
    jump user_forward
  }
}

include "/etc/nftables.d/*.conf"
EOF
sudo systemctl enable --now nftables
`, macHost, macHost, httpsPort, macHost, httpPort,
		ntpNatRule,
		macHost, httpsPort, macHost, httpPort,
		macHost, httpsPort, macHost, httpPort,
		macHost, httpPort, httpsPort,
		macHost, dnsPort,
		ntpFilterRule,
		macHost, httpPort, httpsPort)

	return liveApply + persist
}

// buildTimesyncdScript configures systemd-timesyncd to send NTP
// traffic at the proxy sentinel IP. Sentinel — not MAC_HOST — because
// the guest's `ip daddr <MAC_HOST> return` NAT bypass would otherwise
// fire before our `udp dport 123 dnat` rule, and the packet would
// reach MAC_HOST:123 (where nothing listens) instead of being
// rewritten to the daemon's SNTP responder's random high port. Same
// sentinel iron-proxy uses for DNS answers (see proxySentinelIP in
// vm.go).
//
// Config choices:
//   - No DNS lookup: sentinel is an IP, so timesyncd doesn't resolve
//     anything on every poll.
//   - PollIntervalMaxSec=64 caps the backoff so a Mac wake heals
//     within ~64 seconds even if the previous poll succeeded.
//   - Empty FallbackNTP prevents timesyncd from ever trying the
//     public pool.ntp.org list — the egress firewall would deny it
//     anyway, but silencing the attempt keeps the log clean.
//
// timesyncd is a systemd built-in; no install step needed on Debian.
// `restart` (not `reload`) because timesyncd re-reads its config on
// SIGHUP but not always the drop-in path — a restart is cheap and
// unambiguous.
func buildTimesyncdScript() string {
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
`, proxySentinelIP)
}

// buildDnsmasqScript points dnsmasq's upstream at iron-proxy's DNS
// server. dnsmasq still answers *.test locally; everything else
// forwards to MAC_HOST:DNSPort. iron-proxy returns its own IP for
// every name, so workload resolutions land at MAC_HOST and get
// DNATed by the nftables rules.
//
// dnsmasq binds on 0.0.0.0 (all interfaces), not just 127.0.0.1. This
// makes it reachable from container namespaces: the nftables
// prerouting chain DNAT-redirects container UDP:53 traffic to the
// guest's own port 53, which requires dnsmasq to actually be
// listening on the interface the packet ultimately arrives on
// (docker0, br-*, etc.). Not a security concern — nothing external
// can route to the guest's :53 across vmnet.
//
// systemd-resolved is masked first because it holds :53 by default
// in the cirruslabs/debian template (binds 127.0.0.53 and 127.0.0.54);
// dnsmasq can't start until resolved is gone. /etc/resolv.conf is
// replaced with a plain "nameserver 127.0.0.1" so guest-side tools
// that respect it find dnsmasq via loopback.
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
listen-address=0.0.0.0
bind-interfaces
EOF
sudo systemctl reload-or-restart dnsmasq
`, macHost, dnsPort)
}
