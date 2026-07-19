//go:build darwin

package serviceapi

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("writeFile(%s): %v", path, err)
	}
}

func isImmutable(t *testing.T, path string) bool {
	t.Helper()
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		t.Fatalf("stat(%s): %v", path, err)
	}
	return uint32(st.Flags)&unix.UF_IMMUTABLE != 0
}

func TestLockUnlockConfigFiles_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "devm.yaml"), "x")
	// A locked file blocks its own deletion, so always unlock before
	// t.TempDir's cleanup tries to remove it.
	t.Cleanup(func() { _ = unlockConfigFiles(dir) })

	if err := lockConfigFiles(dir); err != nil {
		t.Fatalf("lock: %v", err)
	}
	if !isImmutable(t, filepath.Join(dir, "devm.yaml")) {
		t.Fatal("devm.yaml not immutable after lock")
	}
	// while locked, a write must fail
	if err := os.WriteFile(filepath.Join(dir, "devm.yaml"), []byte("y"), 0o644); err == nil {
		t.Fatal("expected write to locked file to fail")
	}
	if err := unlockConfigFiles(dir); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	if isImmutable(t, filepath.Join(dir, "devm.yaml")) {
		t.Fatal("still immutable after unlock")
	}
	if err := os.WriteFile(filepath.Join(dir, "devm.yaml"), []byte("y"), 0o644); err != nil {
		t.Fatalf("write after unlock should succeed: %v", err)
	}
}

func TestLockConfigFiles_MissingMeYamlOK(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "devm.yaml"), "x")
	t.Cleanup(func() { _ = unlockConfigFiles(dir) })

	if err := lockConfigFiles(dir); err != nil {
		t.Fatalf("lock with no devm.me.yaml must succeed: %v", err)
	}
}

func TestLockConfigFiles_LocksMeYamlWhenPresent(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "devm.yaml"), "x")
	writeFile(t, filepath.Join(dir, "devm.me.yaml"), "y")
	t.Cleanup(func() { _ = unlockConfigFiles(dir) })

	if err := lockConfigFiles(dir); err != nil {
		t.Fatalf("lock: %v", err)
	}
	if !isImmutable(t, filepath.Join(dir, "devm.me.yaml")) {
		t.Fatal("devm.me.yaml not locked")
	}
}

func TestLockUnlock_Idempotent(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "devm.yaml"), "x")
	t.Cleanup(func() { _ = unlockConfigFiles(dir) })

	if err := lockConfigFiles(dir); err != nil {
		t.Fatal(err)
	}
	if err := lockConfigFiles(dir); err != nil {
		t.Fatalf("double lock: %v", err)
	}
	if err := unlockConfigFiles(dir); err != nil {
		t.Fatal(err)
	}
	if err := unlockConfigFiles(dir); err != nil {
		t.Fatalf("double unlock: %v", err)
	}
}

func TestSetImmutable_MissingFileIsNoop(t *testing.T) {
	dir := t.TempDir()
	if err := setImmutable(filepath.Join(dir, "nope.yaml"), true); err != nil {
		t.Fatalf("setImmutable on missing file: %v", err)
	}
	if err := setImmutable(filepath.Join(dir, "nope.yaml"), false); err != nil {
		t.Fatalf("setImmutable(clear) on missing file: %v", err)
	}
}

func TestSetImmutable_PreservesOtherFlags(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "devm.yaml")
	writeFile(t, path, "x")
	t.Cleanup(func() { _ = setImmutable(path, false) })

	// Set an unrelated flag first (UF_HIDDEN), then lock, then unlock,
	// and confirm the unrelated flag survives both transitions.
	if err := unix.Chflags(path, unix.UF_HIDDEN); err != nil {
		t.Fatalf("seed UF_HIDDEN: %v", err)
	}
	if err := setImmutable(path, true); err != nil {
		t.Fatalf("setImmutable(true): %v", err)
	}
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		t.Fatalf("stat: %v", err)
	}
	if uint32(st.Flags)&unix.UF_HIDDEN == 0 {
		t.Fatal("UF_HIDDEN clobbered by setImmutable(true)")
	}
	if uint32(st.Flags)&unix.UF_IMMUTABLE == 0 {
		t.Fatal("UF_IMMUTABLE not set")
	}

	if err := setImmutable(path, false); err != nil {
		t.Fatalf("setImmutable(false): %v", err)
	}
	if err := unix.Stat(path, &st); err != nil {
		t.Fatalf("stat: %v", err)
	}
	if uint32(st.Flags)&unix.UF_HIDDEN == 0 {
		t.Fatal("UF_HIDDEN clobbered by setImmutable(false)")
	}
	if uint32(st.Flags)&unix.UF_IMMUTABLE != 0 {
		t.Fatal("UF_IMMUTABLE not cleared")
	}
}

func TestConfigLockStore(t *testing.T) {
	s := newConfigLockStore()

	if _, ok := s.get("p1"); ok {
		t.Fatal("expected no entry before put")
	}

	s.put("p1", "/repo/p1")
	e, ok := s.get("p1")
	if !ok || e.repoRoot != "/repo/p1" {
		t.Fatalf("get after put = %+v, %v", e, ok)
	}

	fired1 := make(chan struct{}, 1)
	t1 := time.AfterFunc(time.Hour, func() { fired1 <- struct{}{} })
	s.setTimer("p1", t1)
	e, _ = s.get("p1")
	if e.relock != t1 {
		t.Fatal("setTimer did not store the timer")
	}

	// setTimer again must stop+replace the prior timer, not leak it.
	fired2 := make(chan struct{}, 1)
	t2 := time.AfterFunc(time.Hour, func() { fired2 <- struct{}{} })
	s.setTimer("p1", t2)
	if t1.Stop() {
		t.Fatal("setTimer left the prior timer running")
	}
	e, _ = s.get("p1")
	if e.relock != t2 {
		t.Fatal("setTimer did not replace with the new timer")
	}

	s.stopTimer("p1")
	e, _ = s.get("p1")
	if e.relock != nil {
		t.Fatal("stopTimer did not clear the timer")
	}
	if t2.Stop() {
		t.Fatal("stopTimer left the timer running")
	}

	// del on a project with a live timer must stop it too.
	t3 := time.AfterFunc(time.Hour, func() {})
	s.setTimer("p1", t3)
	s.del("p1")
	if t3.Stop() {
		t.Fatal("del left the timer running")
	}
	if _, ok := s.get("p1"); ok {
		t.Fatal("expected entry gone after del")
	}

	// del/stopTimer on an unknown project must not panic.
	s.del("unknown")
	s.stopTimer("unknown")
}
