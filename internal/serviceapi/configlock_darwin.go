//go:build darwin

package serviceapi

import (
	"os"

	"golang.org/x/sys/unix"
)

// setImmutable flips UF_IMMUTABLE on one file via a read-modify-write
// of its BSD flags, so any other flags already set on the file are
// preserved rather than clobbered. A missing file is a no-op (nil).
// want=true sets the flag; want=false clears it. If the file is
// already in the desired state, chflags is skipped (idempotent).
func setImmutable(path string, want bool) error {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	flags := uint32(st.Flags)
	if want {
		flags |= unix.UF_IMMUTABLE
	} else {
		flags &^= unix.UF_IMMUTABLE
	}
	if flags == uint32(st.Flags) {
		return nil
	}
	return unix.Chflags(path, int(flags))
}
