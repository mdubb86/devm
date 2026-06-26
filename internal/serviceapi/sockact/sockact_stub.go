//go:build !darwin

package sockact

import (
	"errors"
	"net"
)

var ErrNotActivated = errors.New("sockact: launchd socket activation is macOS-only")

func Activate(name string) ([]net.Listener, error) {
	return nil, ErrNotActivated
}
