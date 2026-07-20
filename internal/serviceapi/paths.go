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

	"github.com/mdubb86/devm/internal/identity"
)

// EnsureRuntimeDir creates cfg.RuntimeDir() if it doesn't exist (mode
// 0700). Returns the directory path. Called by the service at startup
// before binding the socket.
func EnsureRuntimeDir(cfg identity.Config) (string, error) {
	dir := cfg.RuntimeDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create runtime dir %s: %w", dir, err)
	}
	return dir, nil
}
