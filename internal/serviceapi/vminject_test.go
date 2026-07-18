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

func TestBuildTimesyncdScript_PointsAtProxySentinel(t *testing.T) {
	script := buildTimesyncdScript()
	assert.Contains(t, script, "/etc/systemd/timesyncd.conf.d/devm.conf")
	// Sentinel: under ENFORCED policy softnet forwards outbound UDP:123
	// to the daemon's SNTP responder regardless of destination IP, so
	// any valid IP reaches it here.
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
