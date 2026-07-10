package serviceapi

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProjectLocks_SameProject_Serializes(t *testing.T) {
	locks := NewProjectLocks()
	var counter int64
	var maxConcurrent int64
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := locks.Lock("proj")
			defer unlock()
			cur := atomic.AddInt64(&counter, 1)
			// Track peak concurrency.
			for {
				m := atomic.LoadInt64(&maxConcurrent)
				if cur <= m || atomic.CompareAndSwapInt64(&maxConcurrent, m, cur) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			atomic.AddInt64(&counter, -1)
		}()
	}
	wg.Wait()
	assert.Equal(t, int64(1), maxConcurrent, "same-project locks must serialize")
}

func TestProjectLocks_DifferentProjects_DontBlock(t *testing.T) {
	locks := NewProjectLocks()
	unlockA := locks.Lock("A")
	defer unlockA()
	done := make(chan struct{})
	go func() {
		unlockB := locks.Lock("B")
		defer unlockB()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		require.Fail(t, "B should not block on A")
	}
}
