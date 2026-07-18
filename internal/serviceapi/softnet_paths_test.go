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
	if !strings.HasPrefix(a, RuntimeDir()) {
		t.Fatalf("control sock %q not under runtime dir %q", a, RuntimeDir())
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
