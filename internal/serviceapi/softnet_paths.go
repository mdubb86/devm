package serviceapi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/mdubb86/devm/internal/identity"
)

// softnetSockDir is a short, per-user location for control sockets — NOT
// nested under cfg.RuntimeDir(). AF_UNIX sun_path is capped at 104 bytes on
// Darwin (103 usable + NUL), and cfg.RuntimeDir() alone can already approach
// that under the e2e harness (`mktemp -d -t devm-e2e-runtime.XXXX` lands
// deep under macOS's per-user $TMPDIR). Rooting at /tmp instead of
// os.TempDir() sidesteps $TMPDIR entirely, since $TMPDIR is exactly the
// long path that overflows the limit. The uid suffix keeps it per-user:
// /tmp is world-writable, so a fixed shared name would let another local
// user pre-create the dir (MkdirAll on an existing dir is a no-op and
// won't fix its mode/owner) before the daemon runs, and the daemon would
// then bind its control socket — the channel carrying LOCKED/OPEN/ENFORCED
// egress-policy commands — inside a directory it doesn't control.
func softnetSockDir() string {
	return "/tmp/devm-softnet-" + strconv.Itoa(os.Getuid())
}

// ensureSoftnetSockDir creates (or verifies) dir as a 0700 directory owned
// by the current user, refusing to use it otherwise. Because the dir name
// embeds the uid (see softnetSockDir), a hostile pre-created directory
// would have to be planted by the same uid that's about to use it — but
// verify ownership and mode anyway rather than trusting MkdirAll's no-op
// silence on a pre-existing directory.
func ensureSoftnetSockDir(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create softnet sock dir: %w", err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("stat softnet sock dir: %w", err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("softnet sock dir %s: cannot verify ownership on this platform", dir)
	}
	if st.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("softnet sock dir %s is owned by uid %d, not the current user (uid %d) — refusing to bind the control socket inside it", dir, st.Uid, os.Getuid())
	}
	if fi.Mode().Perm() != 0700 {
		return fmt.Errorf("softnet sock dir %s has mode %o, want 0700 — refusing to bind the control socket inside it", dir, fi.Mode().Perm())
	}
	return nil
}

// SoftnetControlSock returns the path to the Unix domain socket the
// daemon uses to reach the softnet control channel for projectID.
// Deterministic: the same (cfg.RuntimeDir(), projectID) pair always
// yields the same path, so callers on either end of the socket (the
// daemon spawning softnet, and softnet itself) agree on the location
// without coordination. The path is a hash of cfg.RuntimeDir()+projectID
// rather than the project name itself, both to keep it short regardless
// of project name length and to disambiguate concurrent daemon
// instances (e.g. a real installed daemon and an isolated e2e daemon)
// that might otherwise pick the same project name. Pure: does not touch
// the filesystem, so discoverSoftnet can recompute the same path on
// daemon restart without re-running the ownership/mode check —
// /vm/start is the one caller that creates and validates
// softnetSockDir, via ensureSoftnetSockDir, before spawning softnet.
func SoftnetControlSock(cfg identity.Config, projectID string) string {
	sum := sha256.Sum256([]byte(cfg.RuntimeDir() + "\x00" + projectID))
	return filepath.Join(softnetSockDir(), hex.EncodeToString(sum[:])[:20]+".sock")
}

// ensureSoftnetSymlink materializes <runtimeDir>/softnet-bin/softnet
// as a symlink to the current executable. `tart run --net-softnet`
// resolves a binary literally named "softnet" on the child process's
// $PATH; devm dispatches to softnet mode based on argv[0], so the
// symlink is what makes tart's lookup find devm itself. Idempotent:
// safe to call on every launch, and re-points the link if it's stale
// (e.g. the devm binary moved after an upgrade). Returns binDir so
// the caller can prepend it to the tart child's $PATH.
func ensureSoftnetSymlink(cfg identity.Config) (binDir string, err error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve devm executable: %w", err)
	}

	binDir = filepath.Join(cfg.RuntimeDir(), "softnet-bin")
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
