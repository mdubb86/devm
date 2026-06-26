//go:build darwin

// Package sockact wraps macOS launchd's launch_activate_socket so a
// user-level service can retrieve file descriptors launchd pre-bound
// for it (typically on privileged ports). This is how Ship 3's
// reverse proxy gets :80 and :443 without running as root.
package sockact

/*
#cgo LDFLAGS: -framework CoreFoundation
#include <launch.h>
#include <stdlib.h>
*/
import "C"

import (
	"errors"
	"fmt"
	"net"
	"os"
	"unsafe"
)

// ErrNotActivated is returned when launchd didn't pre-bind any
// sockets under the named entry (e.g., the process was started
// outside launchd or the plist doesn't list this Sockets entry).
var ErrNotActivated = errors.New("sockact: no inherited sockets for this name")

// Activate returns the listeners launchd pre-bound for the named
// entry in the Sockets dict of the running job's plist. Returns
// ErrNotActivated if launchd has nothing for us.
func Activate(name string) ([]net.Listener, error) {
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))

	var fds *C.int
	var cnt C.size_t
	if rc := C.launch_activate_socket(cname, &fds, &cnt); rc != 0 {
		// ESRCH (3) is the launchd code for "no such socket"; we map
		// it to ErrNotActivated so callers can decide whether to fall
		// back. Everything else surfaces as a real error.
		if rc == 3 {
			return nil, ErrNotActivated
		}
		return nil, fmt.Errorf("launch_activate_socket(%s): rc=%d", name, int(rc))
	}
	defer C.free(unsafe.Pointer(fds))

	if cnt == 0 {
		return nil, ErrNotActivated
	}

	cFDs := unsafe.Slice(fds, int(cnt))
	listeners := make([]net.Listener, 0, len(cFDs))
	for _, cfd := range cFDs {
		f := os.NewFile(uintptr(cfd), name)
		l, err := net.FileListener(f)
		// net.FileListener dups the fd; close ours.
		_ = f.Close()
		if err != nil {
			// Clean up listeners we already created.
			for _, prev := range listeners {
				_ = prev.Close()
			}
			return nil, fmt.Errorf("FileListener(%s): %w", name, err)
		}
		listeners = append(listeners, l)
	}
	return listeners, nil
}
