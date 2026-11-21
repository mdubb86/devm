package serviceapi

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/mdubb86/devm/internal/identity"
)

func TestSoftnetControlSockDeterministic(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	a := SoftnetControlSock(identity.Prod, "proj")
	b := SoftnetControlSock(identity.Prod, "proj")
	if a != b || filepath.Base(a) == "" {
		t.Fatalf("non-deterministic: %q %q", a, b)
	}
	// Deliberately NOT nested under RuntimeDir(): AF_UNIX sun_path is
	// capped at 104 bytes on Darwin, and RuntimeDir() alone can already
	// approach that under deep $TMPDIR paths (e.g. the e2e harness).
	if !strings.HasPrefix(a, softnetSockDir()) {
		t.Fatalf("control sock %q not under short per-user dir %q", a, softnetSockDir())
	}
	if len(a) >= 104 {
		t.Fatalf("control sock path %q (%d bytes) exceeds Darwin's 104-byte sun_path limit", a, len(a))
	}

	other := SoftnetControlSock(identity.Prod, "other-proj")
	if other == a {
		t.Fatalf("different project ids collided: %q", a)
	}
}

func TestSoftnetControlSockDisambiguatesRuntimeDirs(t *testing.T) {
	// Two different daemon instances (e.g. a real installed daemon and
	// an isolated e2e daemon) using the same project name must not
	// collide on the same control socket.
	t.Setenv("HOME", t.TempDir())
	a := SoftnetControlSock(identity.Prod, "proj")

	t.Setenv("HOME", t.TempDir())
	b := SoftnetControlSock(identity.Prod, "proj")

	if a == b {
		t.Fatalf("different runtime dirs collided on the same control sock: %q", a)
	}
}

func TestSoftnetSockDir_PerUser(t *testing.T) {
	dir := softnetSockDir()
	want := "/tmp/devm-softnet-" + strconv.Itoa(os.Getuid())
	if dir != want {
		t.Fatalf("softnetSockDir() = %q, want %q (per-uid, not a fixed shared name)", dir, want)
	}
}

// Uses a t.TempDir()-based path, never softnetSockDir() itself: that real
// path is per-uid but shared process-wide (a live daemon running as this
// same uid may have sockets bound under it right now), so a test that
// creates or removes it could yank the rug out from under a real daemon.
func TestEnsureSoftnetSockDir_CreatesOwnedDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sockdir")

	if err := ensureSoftnetSockDir(dir); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0700 {
		t.Fatalf("dir mode = %o, want 0700", fi.Mode().Perm())
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("cannot read Stat_t")
	}
	if st.Uid != uint32(os.Getuid()) {
		t.Fatalf("dir owned by uid %d, want %d", st.Uid, os.Getuid())
	}
}

// A pre-existing dir with loose permissions (e.g. planted by another local
// process before the daemon starts) must be rejected rather than silently
// reused — MkdirAll on an existing dir is a no-op and won't fix its mode,
// so ensureSoftnetSockDir must catch this itself. This is also the
// security property /vm/start now depends on directly: vm.go calls
// ensureSoftnetSockDir(softnetSockDir()) before spawning softnet, and a
// non-nil error here fails the request with 500 rather than binding the
// control socket into an attacker-controlled directory.
func TestEnsureSoftnetSockDir_RejectsLoosePermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sockdir")

	if err := os.MkdirAll(dir, 0777); err != nil {
		t.Fatal(err)
	}

	if err := ensureSoftnetSockDir(dir); err == nil {
		t.Fatal("expected an error for a pre-existing world-writable dir, got nil")
	}
}

func TestEnsureSoftnetSymlink(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	dir, err := ensureSoftnetSymlink(identity.Prod)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "softnet")
	fi, err := os.Lstat(link)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("not a symlink: %v %v", fi, err)
	}
	if _, err := ensureSoftnetSymlink(identity.Prod); err != nil {
		t.Fatalf("not idempotent: %v", err)
	}
}
