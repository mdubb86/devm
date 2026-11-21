package serviceapi

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
