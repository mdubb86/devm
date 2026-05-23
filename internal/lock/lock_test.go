package lock

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAcquireReleaseRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.lock")
	lk, err := Acquire(path)
	require.NoError(t, err)
	require.NoError(t, lk.Release())
}

func TestAcquireIsExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.lock")
	first, err := Acquire(path)
	require.NoError(t, err)

	// Second Acquire should block until first releases. Run in a
	// goroutine with a short timeout.
	got := make(chan *FileLock, 1)
	errCh := make(chan error, 1)
	go func() {
		l, err := Acquire(path)
		if err != nil {
			errCh <- err
			return
		}
		got <- l
	}()

	// Confirm the second Acquire is blocked.
	select {
	case <-got:
		t.Fatal("second Acquire returned before first released")
	case err := <-errCh:
		t.Fatalf("second Acquire errored: %v", err)
	case <-time.After(150 * time.Millisecond):
		// Good — still blocked.
	}

	require.NoError(t, first.Release())

	// Now the second Acquire should complete promptly.
	select {
	case l := <-got:
		require.NoError(t, l.Release())
	case err := <-errCh:
		t.Fatalf("second Acquire errored after first released: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("second Acquire did not unblock within 2s")
	}
}

func TestReleaseIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.lock")
	lk, err := Acquire(path)
	require.NoError(t, err)
	require.NoError(t, lk.Release())
	// Second Release returns nil (no error).
	assert.NoError(t, lk.Release())
}

// Acquire after Release of an earlier holder must succeed. This
// catches the trap of forgetting to close the fd on Release.
func TestAcquireAfterReleaseSucceeds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.lock")
	for i := 0; i < 5; i++ {
		lk, err := Acquire(path)
		require.NoError(t, err)
		require.NoError(t, lk.Release())
	}
}

// Smoke check: two cooperating goroutines acquiring serialize correctly
// and don't double-enter the critical section.
func TestConcurrentSerialize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.lock")
	var mu sync.Mutex // tracks "currently inside critical section"
	insideCount := 0
	maxInside := 0
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lk, err := Acquire(path)
			if err != nil {
				t.Errorf("Acquire failed: %v", err)
				return
			}
			defer lk.Release()
			mu.Lock()
			insideCount++
			if insideCount > maxInside {
				maxInside = insideCount
			}
			mu.Unlock()
			time.Sleep(20 * time.Millisecond)
			mu.Lock()
			insideCount--
			mu.Unlock()
		}()
	}
	wg.Wait()
	assert.Equal(t, 1, maxInside, "lock did not serialize: multiple goroutines held it concurrently")
}
