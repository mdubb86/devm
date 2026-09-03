// Package hostinfo exposes small, read-only queries about the host
// (Mac) that the daemon or CLI needs when starting a VM — currently
// just the local timezone.
package hostinfo

import (
	"fmt"
	"os"
	"strings"
)

// tzSymlink is the path whose target names the current IANA zone. macOS
// ships this as a symlink into the zoneinfo tree.
const tzSymlink = "/etc/localtime"

// zoneinfoPrefixes are the on-disk locations macOS resolves
// /etc/localtime into. `readlink` returns an absolute path with one of
// these prefixes; the IANA zone name is what's left after the prefix.
var zoneinfoPrefixes = []string{
	"/var/db/timezone/zoneinfo/",
	"/usr/share/zoneinfo/",
}

// ResolveMacTimezone returns the Mac's current IANA zone name (e.g.
// "America/Chicago"), read from /etc/localtime. Returns "" with a
// non-nil error when the zone can't be determined — callers should
// treat "" as "leave the guest at UTC" and surface the error at log
// level, not fail-loud, since a missing timezone shouldn't block VM
// startup.
func ResolveMacTimezone() (string, error) {
	target, err := os.Readlink(tzSymlink)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", tzSymlink, err)
	}
	zone, ok := parseFromSymlinkTarget(target)
	if !ok {
		return "", fmt.Errorf("%s target %q does not match any known zoneinfo prefix", tzSymlink, target)
	}
	return zone, nil
}

// parseFromSymlinkTarget extracts the IANA zone name from a
// /etc/localtime symlink target. Extracted from ResolveMacTimezone so
// tests exercise the parsing without depending on the host's actual
// symlink.
func parseFromSymlinkTarget(target string) (string, bool) {
	for _, prefix := range zoneinfoPrefixes {
		if strings.HasPrefix(target, prefix) {
			zone := strings.TrimPrefix(target, prefix)
			if zone == "" {
				return "", false
			}
			return zone, true
		}
	}
	return "", false
}
