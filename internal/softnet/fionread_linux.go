package softnet

import (
	"syscall"
	"unsafe"
)

// fionreadLinux is from <asm-generic/ioctls.h>; both softnet (macOS)
// and CI (Linux) exercise the ingress code path.
const fionreadLinux = 0x541b

func ioctlFIONREAD(fd uintptr) (int, error) {
	var n int32
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL, fd, fionreadLinux,
		uintptr(unsafe.Pointer(&n)),
	)
	if errno != 0 {
		return 0, errno
	}
	return int(n), nil
}
