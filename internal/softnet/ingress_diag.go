package softnet

import (
	"net"
	"syscall"
)

// hostRecvQueueBytes returns the number of bytes queued in the kernel
// receive buffer of hc at the moment of the call, or -1 if the probe
// cannot run. The point is diagnostic: when an ingress splice later
// reports h2g=0 with no error, FIONREAD at accept time proves whether
// the client's bytes had already arrived at the kernel (race-dropped
// by softnet) or had not yet arrived at all (something else).
//
// The FIONREAD constant is BSD-specified (<sys/filio.h> on Darwin,
// <asm-generic/ioctls.h> on Linux) and is defined per-platform in
// fionread_*.go so softnet builds on both macOS (prod) and Linux
// (CI).
func hostRecvQueueBytes(hc net.Conn) int {
	sc, ok := hc.(interface {
		SyscallConn() (syscall.RawConn, error)
	})
	if !ok {
		return -1
	}
	rc, err := sc.SyscallConn()
	if err != nil {
		return -1
	}
	n := -1
	ctrlErr := rc.Control(func(fd uintptr) {
		pending, ioErr := ioctlFIONREAD(fd)
		if ioErr == nil {
			n = pending
		}
	})
	if ctrlErr != nil {
		return -1
	}
	return n
}
