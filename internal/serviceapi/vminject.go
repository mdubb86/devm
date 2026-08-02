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

// buildVolumeMountScript returns the guest-side shell that establishes
// one declared volume: mount the vol_<name> virtiofs share at
// /mnt/vol_<name>/, then decide the four-case action based on
// (wasEmpty, target-has-content) — bind cleanly, adopt then bind,
// or error out with the conflict message.
//
// wasEmpty is the daemon-observed state of the Mac-side volume
// dir BEFORE this boot; the guest can't read the Mac dir directly,
// so the daemon threads this bit in through the script.
//
// Adopt failure is atomic (spec § adopt failure): cp -a runs and its
// failure wipes /mnt/vol_<name>/* back to empty and exits non-zero.
// The Mac dir returns to its pre-attempt state, and provisioning
// aborts. On cp success, the guest target is also evacuated (rm -rf
// its contents) so a subsequent boot — where the bind mount has not
// survived the reboot but the now-adopted Mac content has — sees an
// empty target and a non-empty Mac volume, i.e. a clean bind rather
// than a false both-non-empty conflict. If that evacuation itself
// fails, the Mac side is rolled back and provisioning aborts, same
// as a cp failure.
//
// Idempotent on repeat calls: the outer mountpoint check on the
// target path short-circuits if the bind mount is already present
// from a prior boot in this VM's lifetime. The virtiofs share mount
// and its /etc/fstab entry are each guarded independently too.
//
// The conflict message references $MAC_VOLUME_DIR, a placeholder the
// caller substitutes with the real Mac-side path before injection —
// the guest itself has no visibility into where the share lives on
// the host.
func buildVolumeMountScript(volumeName, guestTargetPath string, wasEmpty bool) string {
	tag := "vol_" + volumeName
	sharePath := "/mnt/vol_" + volumeName

	// The (wasEmpty, target-content) matrix collapses to two
	// scripts because the target-content check only runs in-guest.
	// wasEmpty=true  → the guest chooses between adopt and clean-bind.
	// wasEmpty=false → the guest chooses between error and clean-bind.
	var adoptOrErrorBlock string
	if wasEmpty {
		adoptOrErrorBlock = fmt.Sprintf(`
if [ -n "$(ls -A %s 2>/dev/null)" ]; then
    # Adopt: target has content, Mac volume is empty. Copy target
    # into the volume share (which writes to Mac via virtiofs), then
    # bind. On cp failure, wipe the partial copy so the Mac dir
    # returns to empty (clean-or-nothing) and abort.
    if ! cp -a %s/. %s/; then
        rm -rf %s/*
        echo "volume adopt failed for %s (target=%s); Mac dir rolled back to empty" >&2
        exit 1
    fi
    # Adopt succeeded: evacuate the original target so future boots see
    # it as empty (the bind doesn't persist across guest reboots — only
    # the virtiofs share does — so without this, the next boot hits the
    # both-non-empty conflict path).
    if ! rm -rf %s/*; then
        rm -rf %s/*
        echo "volume adopt failed for %s: could not evacuate target %s; Mac dir rolled back" >&2
        exit 1
    fi
fi
`, guestTargetPath, guestTargetPath, sharePath, sharePath, volumeName, guestTargetPath, guestTargetPath, sharePath, volumeName, guestTargetPath)
	} else {
		adoptOrErrorBlock = fmt.Sprintf(`
if [ -n "$(ls -A %s 2>/dev/null)" ]; then
    cat >&2 <<'CONFLICT_EOF'
mount conflict: volume %s has existing content on the Mac side and
the guest target %s also has content. Resolve one side:
  - clear guest content: devm shell -- sudo rm -rf %s/*
  - clear Mac volume:    rm -rf '$MAC_VOLUME_DIR'
CONFLICT_EOF
    exit 1
fi
`, guestTargetPath, volumeName, guestTargetPath, guestTargetPath)
	}

	return fmt.Sprintf(`set -e
sudo mkdir -p %s
mountpoint -q %s || sudo mount -t virtiofs %s %s
grep -q '^%s ' /etc/fstab || echo '%s %s virtiofs rw,_netdev 0 0' | sudo tee -a /etc/fstab

sudo mkdir -p %s
# Idempotency: if the target is already a bind mount from a previous
# boot, do nothing. The virtiofs share above is idempotent too, so
# re-running the whole script leaves state unchanged.
if mountpoint -q %s; then
    exit 0
fi
%s
sudo mount --bind %s %s
`,
		sharePath, sharePath, tag, sharePath,
		tag, tag, sharePath,
		guestTargetPath,
		guestTargetPath,
		adoptOrErrorBlock,
		sharePath, guestTargetPath,
	)
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
