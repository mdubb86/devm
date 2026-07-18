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

// buildGrowRootScript grows the guest root partition and ext4
// filesystem to fill the virtual disk. Run once on a freshly-cloned
// VM whose disk was enlarged via tart SetDiskSize. growpart, sfdisk,
// and resize2fs live in /sbin, which is not on the default PATH.
// growpart exits non-zero when the partition is already at max, which
// is fine — resize2fs is then a safe no-op. A real resize2fs failure
// still aborts (set -e).
func buildGrowRootScript() string {
	return `set -eo pipefail
PATH=/usr/sbin:/sbin:$PATH growpart /dev/vda 1 || true
PATH=/usr/sbin:/sbin:$PATH resize2fs /dev/vda1
`
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
//
// NODE_EXTRA_CA_CERTS points node at devm's iron-proxy CA. Interactive
// devm.yaml env inheritance covers `devm shell` sessions, but tools that
// SSH in with a raw command (Orca's relay, plain `ssh devm-<vm> <cmd>`)
// bypass that layer. /etc/environment is read by pam_env on ANY PAM
// session, including non-interactive SSH commands, so setting it here
// makes the node trust root system-wide.
func buildEnvScript() string {
	return `sudo tee /etc/environment > /dev/null <<'EOF'
NO_PROXY=*
NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/devm.crt
EOF
`
}

// buildTimesyncdScript configures systemd-timesyncd to send NTP
// traffic at the proxy sentinel IP. Under ENFORCED policy softnet
// forwards outbound UDP:123 to the daemon's SNTP responder regardless
// of destination IP, so any valid IP here reaches it — this reuses the
// same sentinel iron-proxy uses for DNS answers (see proxySentinelIP in
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
