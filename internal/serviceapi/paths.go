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

	"github.com/mdubb86/devm/internal/identity"
)

// MutagenSSHDirName is the subdirectory under the daemon runtime dir
// that holds the tart-mutagen-ssh shim (as `ssh` and `scp`).
// MUTAGEN_SSH_PATH points here for the mutagen daemon.
const MutagenSSHDirName = "mutagen-ssh-dir"

// MutagenSSHDir returns <runtime-dir>/mutagen-ssh-dir. The shim installs
// under this path as `ssh` and `scp`; mutagen's env-based binary lookup
// finds them.
func MutagenSSHDir(cfg identity.Config) string {
	return filepath.Join(cfg.RuntimeDir(), MutagenSSHDirName)
}

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
