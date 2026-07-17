package serviceapi

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildGrowRootScript(t *testing.T) {
	s := buildGrowRootScript()
	assert.Contains(t, s, "growpart /dev/vda 1")
	assert.Contains(t, s, "resize2fs /dev/vda1")
	// PATH must include /sbin so growpart finds sfdisk and resize2fs.
	assert.Contains(t, s, "/sbin")
	// growpart's no-op exit must be tolerated.
	assert.True(t, strings.Contains(s, "growpart /dev/vda 1 || true"))
}

func TestBuildWorkspaceMountScript_MountsVirtioFSAtMirrorPath(t *testing.T) {
	path := "/Users/michael/workspace/myproject"
	script := buildWorkspaceMountScript(path)
	assert.Contains(t, script, "mount -t virtiofs workspace "+path)
	assert.Contains(t, script, "mkdir -p "+path)
	assert.Contains(t, script, "workspace "+path+" virtiofs")
	// Explicit: no chown — virtiofs on Apple Virtualization.framework rejects
	// guest-side ownership changes with "Operation not permitted".
	assert.NotContains(t, script, "chown")
}

func TestParseExtraMounts_RWAndRO(t *testing.T) {
	got := parseExtraMounts([]string{
		"/Users/x/data",
		"/Users/x/ro-thing:ro",
		"", // dropped
	})
	require.Len(t, got, 2)
	assert.Equal(t, extraMount{hostPath: "/Users/x/data", readOnly: false}, got[0])
	assert.Equal(t, extraMount{hostPath: "/Users/x/ro-thing", readOnly: true}, got[1])
}

func TestBuildExtraMountScript_RW(t *testing.T) {
	script := buildExtraMountScript("extra_0", "/Users/x/data", false)
	assert.Contains(t, script, "mkdir -p /Users/x/data")
	assert.Contains(t, script, "mount -t virtiofs extra_0 /Users/x/data")
	assert.Contains(t, script, "extra_0 /Users/x/data virtiofs rw,_netdev 0 0")
	// RW must not pass -o ro to mount.
	assert.NotContains(t, script, "-o ro")
}

func TestBuildExtraMountScript_ReadOnly(t *testing.T) {
	script := buildExtraMountScript("extra_1", "/Users/x/ro-thing", true)
	assert.Contains(t, script, "mount -o ro -t virtiofs extra_1 /Users/x/ro-thing")
	assert.Contains(t, script, "extra_1 /Users/x/ro-thing virtiofs ro,_netdev 0 0")
}

func TestBuildEnvScript_SetsSystemWideEnvVars(t *testing.T) {
	script := buildEnvScript()
	assert.Contains(t, script, "NO_PROXY=*")
	// NODE_EXTRA_CA_CERTS makes non-interactive SSH sessions (Orca's
	// relay, plain `ssh devm-<vm> <cmd>`) trust iron-proxy's re-signed
	// certs without inheriting from devm.yaml's `env:` block.
	assert.Contains(t, script, "NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/devm.crt")
	// Old HTTPS_PROXY-style assertions removed — the transparent
	// model doesn't use env vars.
	assert.NotContains(t, script, "HTTPS_PROXY=http")
	assert.NotContains(t, script, "HTTP_PROXY=http")
}

func TestBuildNftablesScript_DNATsHTTPAndHTTPS(t *testing.T) {
	script := buildNftablesScript("192.168.64.1", 8080, 8443, 8053, 0, false)
	// NAT table redirects :443 and :80 to MAC_HOST:proxy_ports.
	assert.Contains(t, script, "table ip devm_nat")
	assert.Contains(t, script, "tcp dport 443 dnat to 192.168.64.1:8443")
	assert.Contains(t, script, "tcp dport 80 dnat to 192.168.64.1:8080")
	// Bypass for traffic already destined for MAC_HOST.
	assert.Contains(t, script, "ip daddr 192.168.64.1 return")
}

func TestBuildNftablesScript_FilterDefaultDenyExceptIronProxyPorts(t *testing.T) {
	script := buildNftablesScript("192.168.64.1", 8080, 8443, 8053, 0, false)
	assert.Contains(t, script, "table inet devm_filter")
	assert.Contains(t, script, "policy drop;")
	// All three iron-proxy ports allowed to MAC_HOST.
	assert.Contains(t, script, "192.168.64.1")
	assert.Contains(t, script, "8080")
	assert.Contains(t, script, "8443")
	assert.Contains(t, script, "8053")
	// The provisioner — not this script — owns which unit restores
	// enforcement on boot (setupBootEnforcement enables either
	// nftables.service or devm-enforce.service depending on startup:).
	assert.NotContains(t, script, "systemctl enable --now nftables")
}

func TestBuildNftablesRuleset_RulesetBodyWithoutPersistence(t *testing.T) {
	// The boot-integrity-gate provisioning script bakes ONLY the ruleset
	// body (the `nft -f -` content) into its enforce-phase heredoc — the
	// daemon live-applies it every provision while the base image's
	// skeleton stays the boot lock, so the /etc/nftables.conf persistence
	// block must NOT be present.
	ruleset := buildNftablesRuleset("192.168.64.1", 8080, 8443, 8053, 0, false)
	// The allowlist policy is present.
	assert.Contains(t, ruleset, "add table inet devm_filter")
	assert.Contains(t, ruleset, "tcp dport 443 dnat to 192.168.64.1:8443")
	assert.Contains(t, ruleset, "add rule inet devm_filter forward jump svc_ingress")
	// But NOT the heredoc wrapper or the persistence block.
	assert.NotContains(t, ruleset, "sudo nft -f -")
	assert.NotContains(t, ruleset, "/etc/nftables.conf")
	assert.NotContains(t, ruleset, "include \"/etc/nftables.d/*.conf\"")

	// buildNftablesScript embeds exactly this ruleset (single source of
	// truth for the policy).
	full := buildNftablesScript("192.168.64.1", 8080, 8443, 8053, 0, false)
	assert.Contains(t, full, ruleset)
	assert.Contains(t, full, "/etc/nftables.conf") // full script DOES persist
}

func TestBuildNftablesScript_NTPPortAddsDNATAndFilterRule(t *testing.T) {
	// ntpPort > 0 → DNAT for UDP:123 to MAC_HOST:ntpPort and a matching
	// filter accept. Guest's timesyncd sends to whatever it thinks is NTP;
	// nftables rewrites the destination transparently.
	script := buildNftablesScript("192.168.64.1", 8080, 8443, 8053, 51234, false)
	assert.Contains(t, script, "udp dport 123 dnat to 192.168.64.1:51234")
	assert.Contains(t, script, "ip daddr 192.168.64.1 udp dport 51234 accept")
}

func TestBuildNftablesScript_ZeroNTPPortSkipsRule(t *testing.T) {
	// ntpPort == 0 (unit-test path) must not emit a broken `dnat to
	// MAC_HOST:0` — that would nftables-reject and refuse to load.
	script := buildNftablesScript("192.168.64.1", 8080, 8443, 8053, 0, false)
	assert.NotContains(t, script, "udp dport 123 dnat")
	assert.NotContains(t, script, ":0 ") // paranoia — no zero-port rule anywhere
}

func TestBuildNftablesScript_NoUserEscapeHatch(t *testing.T) {
	for _, docker := range []bool{true, false} {
		s := buildNftablesScript("192.168.64.1", 8080, 8443, 8053, 51234, docker)
		assert.NotContains(t, s, "user_output", "user_output chain must be gone")
		assert.NotContains(t, s, "user_forward", "user_forward chain must be gone")
		// svc_ingress is retained.
		assert.Contains(t, s, "svc_ingress", "svc_ingress must remain")
	}
}

func TestBuildNftablesScript_PersistsViaIncludeGlob(t *testing.T) {
	// svc_ingress survives VM reboot because:
	//   (1) apply-egress-enforcement snapshots its live state to
	//       /etc/nftables.d/svc_ingress.conf, and
	//   (2) /etc/nftables.conf has `include "/etc/nftables.d/*.conf"`
	//       so systemd's nftables.service pulls the snapshot back in
	//       on the next boot.
	script := buildNftablesScript("192.168.64.1", 8080, 8443, 8053, 51234, false)
	assert.Contains(t, script, "nft list chain inet devm_filter svc_ingress > /etc/nftables.d/svc_ingress.conf")
	assert.Contains(t, script, `include "/etc/nftables.d/*.conf"`)
	// The persisted config must ALSO declare svc_ingress so the
	// forward chain's `jump` target exists at load time. Declared
	// before include so the include's rules merge into it.
	assert.Contains(t, script, "chain svc_ingress {}")
	assert.Contains(t, script, "jump svc_ingress")
}

func TestBuildNftablesScript_DockerAddsBridgeAccept(t *testing.T) {
	with := buildNftablesScript("192.168.64.1", 8080, 8443, 8053, 0, true)
	assert.Contains(t, with, "ip daddr 172.16.0.0/12 accept",
		"docker=true must emit the container-bridge egress accept")
	// present in BOTH the live-apply and the persisted /etc/nftables.conf.
	assert.GreaterOrEqual(t, strings.Count(with, "ip daddr 172.16.0.0/12 accept"), 2,
		"the accept must be in both the live-apply block and /etc/nftables.conf")

	without := buildNftablesScript("192.168.64.1", 8080, 8443, 8053, 0, false)
	assert.NotContains(t, without, "172.16.0.0/12",
		"docker=false must not open the container-bridge range")
}

func TestBuildNftablesScript_ForwardChainRedirectsContainerTraffic(t *testing.T) {
	// Container→internet packets traverse the FORWARD hook (they came
	// from a docker bridge, headed for eth0). Our forward nat chain in
	// PREROUTING must DNAT-redirect their HTTP/HTTPS to iron-proxy and
	// their DNS to the guest's own port 53 (dnsmasq). Scoped by
	// iifname so packets arriving from the Mac aren't touched.
	script := buildNftablesScript("192.168.64.1", 8080, 8443, 8053, 51234, false)

	// DNS redirect: container→any-DNS-server:53 → local :53 (dnsmasq
	// bound on 0.0.0.0). `redirect to :53` translates automatically to
	// the local machine's IP, so it works regardless of which docker
	// bridge the packet came from.
	assert.Contains(t, script, `iifname { "docker0", "docker_gwbridge" } udp dport 53 redirect to :53`)
	assert.Contains(t, script, `iifname "br-*" udp dport 53 redirect to :53`)
	// HTTPS/HTTP DNAT: container→any:443/80 → MAC_HOST:iron-proxy
	assert.Contains(t, script, `iifname { "docker0", "docker_gwbridge" } tcp dport 443 dnat to 192.168.64.1:8443`)
	assert.Contains(t, script, `iifname "br-*" tcp dport 443 dnat to 192.168.64.1:8443`)
	assert.Contains(t, script, `iifname { "docker0", "docker_gwbridge" } tcp dport 80 dnat to 192.168.64.1:8080`)
	assert.Contains(t, script, `iifname "br-*" tcp dport 80 dnat to 192.168.64.1:8080`)
}

func TestBuildNftablesScript_ForwardFilterDefaultDeny(t *testing.T) {
	// The forward filter chain closes container→internet on
	// non-DNAT'd ports (e.g., a container trying to SSH out). Base
	// rules only accept established/related plus post-DNAT traffic to
	// MAC_HOST:iron-proxy — everything else drops.
	script := buildNftablesScript("192.168.64.1", 8080, 8443, 8053, 51234, false)
	assert.Contains(t, script, "add chain inet devm_filter forward { type filter hook forward priority 0 ; policy drop ; }")
	assert.Contains(t, script, "add rule inet devm_filter forward ct state established,related accept")
	assert.Contains(t, script, "add rule inet devm_filter forward ip daddr 192.168.64.1 tcp dport { 8080, 8443 } accept")
	assert.Contains(t, script, "add rule inet devm_filter forward jump svc_ingress")
}

func TestBuildNftablesScript_ForwardChainPersisted(t *testing.T) {
	// The persisted /etc/nftables.conf must contain the forward and
	// prerouting chains so systemd's nftables.service restores them on
	// boot.
	script := buildNftablesScript("192.168.64.1", 8080, 8443, 8053, 51234, false)
	assert.Contains(t, script, "chain forward {")
	assert.Contains(t, script, "chain prerouting {")
}

func TestNftablesScaffoldsSvcIngress(t *testing.T) {
	script := buildNftablesScript("192.168.64.1", 40000, 40001, 40002, 0, false)
	// Chain declared and jumped from forward.
	assert.Contains(t, script, "add chain inet devm_filter svc_ingress")
	assert.Contains(t, script, "add rule inet devm_filter forward jump svc_ingress")
	// Persist half declares the chain and jumps it too.
	assert.Contains(t, script, "chain svc_ingress {}")
}

func TestBuildTimesyncdScript_PointsAtProxySentinel(t *testing.T) {
	script := buildTimesyncdScript()
	assert.Contains(t, script, "/etc/systemd/timesyncd.conf.d/devm.conf")
	// Sentinel (not MAC_HOST): the ip-daddr-MAC_HOST-return NAT bypass
	// would otherwise fire before our udp:123 DNAT and the packet
	// would arrive at MAC_HOST:123 (no listener) instead of the
	// daemon's SNTP responder.
	assert.Contains(t, script, "NTP="+proxySentinelIP)
	// Explicit empty FallbackNTP prevents timesyncd from ever trying
	// the default pool.ntp.org list; egress firewall would deny it
	// anyway, but silencing the attempt keeps the log clean.
	assert.Contains(t, script, "FallbackNTP=")
	// Poll interval cap: even if timesyncd's exponential backoff
	// climbs, the max is bounded — post-wake heal is ~64s worst case.
	assert.Contains(t, script, "PollIntervalMaxSec=64")
	assert.Contains(t, script, "systemctl restart systemd-timesyncd")
}

func TestBuildDnsmasqScript_ForwardsToIronProxyDNS(t *testing.T) {
	script := buildDnsmasqScript("192.168.64.1", 8053)
	assert.Contains(t, script, "systemctl mask --now systemd-resolved")
	assert.Contains(t, script, "no-resolv")
	assert.Contains(t, script, "server=192.168.64.1#8053")
	assert.Contains(t, script, "address=/test/127.0.0.1")
	assert.Contains(t, script, "nameserver 127.0.0.1")
	assert.Contains(t, script, "systemctl reload-or-restart dnsmasq")
	// bind-dynamic makes dnsmasq listen on all interface addresses and
	// re-bind whenever new interfaces come up. Required for containers:
	// docker0 doesn't exist at dnsmasq's initial start (Docker installs
	// later in provisioning), so a static bind mode would miss it and
	// the nftables prerouting DNAT for container UDP:53 would land at
	// an interface dnsmasq isn't listening on.
	assert.Contains(t, script, "bind-dynamic")
}
