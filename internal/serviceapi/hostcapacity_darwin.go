//go:build darwin

package serviceapi

import "golang.org/x/sys/unix"

// hostCapacity reports this Mac's total RAM in bytes and virtual CPU
// count, straight from the kernel via sysctl. StartVM uses it to reject
// a memory:/cpu: override the host can't satisfy before handing it to
// tart.
func hostCapacity() (memBytes uint64, cpus uint32, err error) {
	memBytes, err = unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0, 0, err
	}
	cpus, err = unix.SysctlUint32("hw.ncpu")
	if err != nil {
		return 0, 0, err
	}
	return memBytes, cpus, nil
}
