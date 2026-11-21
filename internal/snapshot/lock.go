package snapshot

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Lock takes an exclusive flock on <dir>/.lock. The returned unlock
// function releases the flock and closes the file descriptor. Lock
// blocks until the holder releases — no deadline. Callers wrap with
// a context timeout where a lifecycle path cannot wait indefinitely
// (snapshot capture from an on_stop hook, for example).
func Lock(dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("mkdir lock dir %s: %w", dir, err)
	}
	path := filepath.Join(dir, ".lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("open lock %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("flock %s: %w", path, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
