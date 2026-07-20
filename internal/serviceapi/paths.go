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

// runtimeDirEnv is a legacy env-var override for the runtime dir,
// retained temporarily so incremental commits compile. Used by e2e to
// sandbox the test daemon away from the user's real `~/Library/
// Application Support/devm/` — without it, running `just e2e-devm`
// runs `devm uninstall` on the user's real daemon and wipes every
// project's iron-proxy config. Removed in a later commit once every
// caller passes cfg explicitly end-to-end (Task 6).
const runtimeDirEnv = "DEVM_RUNTIME_DIR"

// RuntimeDir returns cfg's runtime dir, honoring $DEVM_RUNTIME_DIR
// when set. Exported (rather than folded into EnsureRuntimeDir) so
// sibling packages (sshconfig, sshkeys) and CLI-side helpers resolve
// to the same location as the daemon without duplicating the
// override logic. Legacy; removed in a later commit once every
// caller passes cfg explicitly and the env override goes away.
func RuntimeDir(cfg identity.Config) string {
	if p := os.Getenv(runtimeDirEnv); p != "" {
		return p
	}
	return cfg.RuntimeDir()
}

// SocketPath returns the absolute path to the Unix domain socket the
// service listens on. 0600 perms enforced at bind time.
func SocketPath(cfg identity.Config) string {
	return filepath.Join(RuntimeDir(cfg), "devm.sock")
}

// EnsureRuntimeDir creates cfg.RuntimeDir() if it doesn't exist (mode
// 0700). Returns the directory path. Called by the service at startup
// before binding the socket.
func EnsureRuntimeDir(cfg identity.Config) (string, error) {
	dir := RuntimeDir(cfg)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create runtime dir %s: %w", dir, err)
	}
	return dir, nil
}
