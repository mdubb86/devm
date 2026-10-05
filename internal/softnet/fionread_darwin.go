package softnet

import (
	"syscall"
	"unsafe"
)

// fionreadDarwin is _IOR('f', 127, int) from <sys/filio.h>.
const fionreadDarwin = 0x4004667f

func ioctlFIONREAD(fd uintptr) (int, error) {
	var n int32
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL, fd, fionreadDarwin,
		uintptr(unsafe.Pointer(&n)),
	)
	if errno != 0 {
		return 0, errno
	}
	return int(n), nil
}
