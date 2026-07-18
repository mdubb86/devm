package serviceapi

import (
	"os"
	"path/filepath"
	"strings"
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
	if !strings.HasPrefix(a, softnetSockDir) {
		t.Fatalf("control sock %q not under fixed short dir %q", a, softnetSockDir)
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
