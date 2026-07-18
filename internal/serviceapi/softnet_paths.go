package serviceapi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// softnetSockDir is a short, fixed location for control sockets — NOT
// nested under RuntimeDir(). AF_UNIX sun_path is capped at 104 bytes on
// Darwin (103 usable + NUL), and RuntimeDir() alone can already approach
// that under the e2e harness (`mktemp -d -t devm-e2e-runtime.XXXX` lands
// deep under macOS's per-user $TMPDIR). Rooting at /tmp instead of
// os.TempDir() sidesteps $TMPDIR entirely, since $TMPDIR is exactly the
// long path that overflows the limit.
const softnetSockDir = "/tmp/devm-softnet"

// SoftnetControlSock returns the path to the Unix domain socket the
// daemon uses to reach the softnet control channel for projectID.
// Deterministic: the same (RuntimeDir, projectID) pair always yields the
// same path, so callers on either end of the socket (the daemon spawning
// softnet, and softnet itself) agree on the location without
// coordination. The path is a hash of RuntimeDir()+projectID rather than
// the project name itself, both to keep it short regardless of project
// name length and to disambiguate concurrent daemon instances (e.g. a
// real installed daemon and an isolated e2e daemon) that might otherwise
// pick the same project name. Ensures the parent directory exists (mode
// 0700) as a best effort; callers that need to observe a MkdirAll
// failure should stat the returned path themselves.
func SoftnetControlSock(projectID string) string {
	_ = os.MkdirAll(softnetSockDir, 0700)
	sum := sha256.Sum256([]byte(RuntimeDir() + "\x00" + projectID))
	return filepath.Join(softnetSockDir, hex.EncodeToString(sum[:])[:20]+".sock")
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
