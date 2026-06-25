// Package serviceapi implements the devm Mac-side service: its HTTP
// API over a Unix domain socket, the CLI-side client that talks to
// it, and the oklog/run composition that wires the actors together.
//
// Ship 1 only exposes /health and /version. Later ships add endpoints
// for DNS, routing, sandbox lifecycle, etc.
package serviceapi

import (
	"fmt"
	"os"
	"path/filepath"
)

// SocketPath returns the absolute path to the Unix domain socket
// the service listens on. Per-user (under $HOME), 0600 perms enforced
// at bind time. Stable across invocations so the CLI knows where to
// connect.
func SocketPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Application Support", "devm", "devm.sock")
}

// EnsureRuntimeDir creates the parent directory of the socket if it
// doesn't exist (mode 0700). Returns the directory path. Called by
// the service at startup before binding the socket.
func EnsureRuntimeDir() (string, error) {
	dir := filepath.Dir(SocketPath())
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create runtime dir %s: %w", dir, err)
	}
	return dir, nil
}
