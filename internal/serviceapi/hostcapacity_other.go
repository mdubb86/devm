//go:build !darwin

package serviceapi

import "errors"

// hostCapacity is unavailable outside darwin: hw.memsize/hw.ncpu are BSD
// sysctl names. This stub just lets the package build and vet during
// cross-platform CI; StartVM treats the error as "skip the check".
func hostCapacity() (memBytes uint64, cpus uint32, err error) {
	return 0, 0, errors.New("host-capacity check unavailable on this platform")
}
