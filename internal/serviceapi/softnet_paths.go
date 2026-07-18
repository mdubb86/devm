package serviceapi

import (
	"fmt"
	"os"
	"path/filepath"
)

// SoftnetControlSock returns the path to the Unix domain socket the
// daemon uses to reach the softnet control channel for projectID.
// Deterministic: the same projectID always yields the same path, so
// callers on either end of the socket (the daemon spawning softnet,
// and softnet itself) agree on the location without coordination.
// Ensures the parent directory exists (mode 0700) as a best effort;
// callers that need to observe a MkdirAll failure should stat the
// returned path themselves.
func SoftnetControlSock(projectID string) string {
	dir := filepath.Join(RuntimeDir(), "softnet")
	_ = os.MkdirAll(dir, 0700)
	return filepath.Join(dir, fmt.Sprintf("%s.sock", projectID))
}

// ensureSoftnetSymlink materializes <runtimeDir>/softnet-bin/softnet
// as a symlink to the current executable. `tart run --net-softnet`
// resolves a binary literally named "softnet" on the child process's
// $PATH; devm dispatches to softnet mode based on argv[0], so the
// symlink is what makes tart's lookup find devm itself. Idempotent:
// safe to call on every launch, and re-points the link if it's stale
// (e.g. the devm binary moved after an upgrade). Returns binDir so
// the caller can prepend it to the tart child's $PATH.
func ensureSoftnetSymlink() (binDir string, err error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve devm executable: %w", err)
	}

	binDir = filepath.Join(RuntimeDir(), "softnet-bin")
	if err := os.MkdirAll(binDir, 0700); err != nil {
		return "", fmt.Errorf("create softnet bin dir: %w", err)
	}

	link := filepath.Join(binDir, "softnet")
	if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("remove stale softnet symlink: %w", err)
	}
	if err := os.Symlink(exe, link); err != nil {
		return "", fmt.Errorf("symlink softnet: %w", err)
	}

	return binDir, nil
}
