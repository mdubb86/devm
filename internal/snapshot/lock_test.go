package snapshot

import (
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLock_CreatesLockFile(t *testing.T) {
	dir := t.TempDir()
	unlock, err := Lock(dir)
	require.NoError(t, err)
	t.Cleanup(unlock)
	assert.FileExists(t, filepath.Join(dir, ".lock"))
}

func TestLock_CreatesDirIfMissing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "deeper")
	unlock, err := Lock(dir)
	require.NoError(t, err)
	t.Cleanup(unlock)
	assert.FileExists(t, filepath.Join(dir, ".lock"))
}

// A concurrent cron run and a CLI run on the same entry must not
// interleave — the second Lock blocks until the first unlocks.
func TestLock_IsExclusive(t *testing.T) {
	dir := t.TempDir()
	unlock, err := Lock(dir)
	require.NoError(t, err)

	var acquired atomic.Bool
	done := make(chan struct{})
	go func() {
		unlock2, err := Lock(dir)
		if err == nil {
			acquired.Store(true)
			unlock2()
		}
		close(done)
	}()

	// Give the goroutine time to block on the lock.
	time.Sleep(100 * time.Millisecond)
	assert.False(t, acquired.Load(), "second Lock should block while first is held")

	unlock()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("second Lock did not acquire after first released")
	}
	assert.True(t, acquired.Load())
}
