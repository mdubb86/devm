package serviceapi

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestSoftnetControlSockDeterministic(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", t.TempDir())

	a := SoftnetControlSock("proj")
	b := SoftnetControlSock("proj")
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

	other := SoftnetControlSock("other-proj")
	if other == a {
		t.Fatalf("different project ids collided: %q", a)
	}
}

func TestSoftnetControlSockDisambiguatesRuntimeDirs(t *testing.T) {
	// Two different daemon instances (e.g. a real installed daemon and
	// an isolated e2e daemon) using the same project name must not
	// collide on the same control socket.
	t.Setenv("DEVM_RUNTIME_DIR", t.TempDir())
	a := SoftnetControlSock("proj")

	t.Setenv("DEVM_RUNTIME_DIR", t.TempDir())
	b := SoftnetControlSock("proj")

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

func TestEnsureSoftnetSockDir_CreatesOwnedDir(t *testing.T) {
	dir := softnetSockDir()
	defer os.RemoveAll(dir)
	os.RemoveAll(dir) // start clean regardless of prior test/process state

	got, err := ensureSoftnetSockDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Fatalf("got %q, want %q", got, dir)
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

// A pre-existing dir at the well-known path with loose permissions (e.g.
// planted by another local process before the daemon starts) must be
// rejected rather than silently reused — MkdirAll on an existing dir is a
// no-op and won't fix its mode, so ensureSoftnetSockDir must catch this
// itself.
func TestEnsureSoftnetSockDir_RejectsLoosePermissions(t *testing.T) {
	dir := softnetSockDir()
	defer os.RemoveAll(dir)
	os.RemoveAll(dir)

	if err := os.MkdirAll(dir, 0777); err != nil {
		t.Fatal(err)
	}

	if _, err := ensureSoftnetSockDir(); err == nil {
		t.Fatal("expected an error for a pre-existing world-writable dir, got nil")
	}
}

func TestEnsureSoftnetSymlink(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", t.TempDir())

	dir, err := ensureSoftnetSymlink()
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "softnet")
	fi, err := os.Lstat(link)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("not a symlink: %v %v", fi, err)
	}
	if _, err := ensureSoftnetSymlink(); err != nil {
		t.Fatalf("not idempotent: %v", err)
	}
}
