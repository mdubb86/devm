// Package lock provides a project-local file lock used to serialize
// devm CLI invocations (shell / stop / teardown) against each other
// during the sandbox bootstrap window. The lock is released as soon
// as the orchestrator hands off to the user's interactive session;
// it does NOT span the user's session lifetime.
//
// Uses POSIX advisory file locking via syscall.Flock; works on macOS
// (where devm CLI runs) and Linux. Acquire is blocking with no
// timeout — the caller relies on the user interrupting with Ctrl-C if
// a holder hangs indefinitely.
package lock

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// FileLock holds an exclusive advisory lock on a file. Always Release
// when done — losing the FileLock value without releasing leaks an
// open file descriptor until the OS reclaims it.
type FileLock struct {
	f  *os.File
	mu sync.Mutex
}

// Acquire opens (creating if necessary) the file at path and obtains
// an exclusive advisory lock on it. Blocks until the lock is acquired.
// The file's parent directory must exist or be creatable.
func Acquire(path string) (*FileLock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("lock: mkdir parent: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("lock: open %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock: flock %s: %w", path, err)
	}
	return &FileLock{f: f}, nil
}

// Release drops the lock and closes the underlying file. Safe to call
// multiple times; subsequent calls return nil.
func (l *FileLock) Release() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	// Unflock is not strictly required — close() releases the lock —
	// but explicit is kinder to readers.
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	err := l.f.Close()
	l.f = nil
	return err
}
