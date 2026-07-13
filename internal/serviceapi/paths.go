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

// runtimeDirEnv is the environment variable that overrides the
// default runtime directory. Used by e2e to sandbox the test daemon
// away from the user's real `~/Library/Application Support/devm/` —
// without it, running `just e2e-devm` runs `devm uninstall` on the
// user's real daemon and wipes every project's iron-proxy config.
const runtimeDirEnv = "DEVM_RUNTIME_DIR"

// RuntimeDir returns the base directory devm reads and writes runtime
// state to (socket, iron-proxy configs, state snapshots, CA material).
// Respects $DEVM_RUNTIME_DIR when set; otherwise falls back to the
// default per-user location. Exposed so CLI-side helpers (e.g. the
// orchestrator's CA cert reader) resolve to the same location as the
// daemon side without importing the whole paths module.
func RuntimeDir() string {
	if p := os.Getenv(runtimeDirEnv); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Application Support", "devm")
}

// SocketPath returns the absolute path to the Unix domain socket
// the service listens on. 0600 perms enforced at bind time. Stable
// across invocations so the CLI knows where to connect. Overridable
// via $DEVM_RUNTIME_DIR.
func SocketPath() string {
	return filepath.Join(RuntimeDir(), "devm.sock")
}

// EnsureRuntimeDir creates the runtime directory if it doesn't exist
// (mode 0700). Returns the directory path. Called by the service at
// startup before binding the socket.
func EnsureRuntimeDir() (string, error) {
	dir := RuntimeDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create runtime dir %s: %w", dir, err)
	}
	return dir, nil
}
