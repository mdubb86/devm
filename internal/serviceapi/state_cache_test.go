package serviceapi

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStateCache_NewIsEmpty(t *testing.T) {
	c := NewStateCache()
	assert.Empty(t, c.AllProjectRows())
	g := c.Global()
	assert.Equal(t, 0, g.MutagenDaemonPID)
	assert.False(t, g.ProxyReady)
}

func TestStateCache_SetMacCwd_ThenGet(t *testing.T) {
	c := NewStateCache()
	c.SetMacCwd("proj", "/Users/x/proj")
	row, ok := c.ProjectRow("proj")
	require.True(t, ok)
	assert.Equal(t, "/Users/x/proj", row.MacCwd)
}

func TestStateCache_SetVMState_TransitionsCorrectly(t *testing.T) {
	c := NewStateCache()
	c.SetMacCwd("p", "/x")
	c.SetVMState("p", VMRunning)
	row, _ := c.ProjectRow("p")
	assert.Equal(t, VMRunning, row.VMState)
	c.SetVMState("p", VMStopped)
	row, _ = c.ProjectRow("p")
	assert.Equal(t, VMStopped, row.VMState)
}

func TestStateCache_ProjectRow_UnknownReturnsFalse(t *testing.T) {
	c := NewStateCache()
	row, ok := c.ProjectRow("missing")
	assert.False(t, ok)
	assert.Equal(t, ProjectRow{}, row)
}

func TestStateCache_AllProjectRows_ReturnsCopy(t *testing.T) {
	c := NewStateCache()
	c.SetMacCwd("p", "/x")
	m := c.AllProjectRows()
	m["p"] = ProjectRow{MacCwd: "/mutated"}
	row, _ := c.ProjectRow("p")
	assert.Equal(t, "/x", row.MacCwd, "AllProjectRows must return a copy; caller mutation must not leak")
}

func TestStateCache_RemoveProject_RemovesRow(t *testing.T) {
	c := NewStateCache()
	c.SetMacCwd("p", "/x")
	c.RemoveProject("p")
	_, ok := c.ProjectRow("p")
	assert.False(t, ok)
}

func TestStateCache_TouchProjectReconciled_UpdatesTimestamp(t *testing.T) {
	c := NewStateCache()
	c.SetMacCwd("p", "/x")
	before := time.Now()
	c.TouchProjectReconciled("p")
	row, _ := c.ProjectRow("p")
	assert.False(t, row.LastReconciledAt.Before(before))
}

func TestStateCache_Global_SetBuildAndProxyReady(t *testing.T) {
	c := NewStateCache()
	b := Build{Version: "0.99.0", Commit: "abc", Date: "2026-09-08"}
	c.SetBuild(b)
	c.SetProxyReady(true)
	c.SetMutagenDaemonPID(4242)
	g := c.Global()
	assert.Equal(t, b, g.Build)
	assert.True(t, g.ProxyReady)
	assert.Equal(t, 4242, g.MutagenDaemonPID)
}

func TestStateCache_ConcurrentReadWrite_RaceFree(t *testing.T) {
	// Verifies RWMutex + copy-on-return semantics under -race.
	c := NewStateCache()
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					c.SetMacCwd("p", "/x")
					c.SetVMState("p", VMRunning)
					_, _ = c.ProjectRow("p")
					_ = c.AllProjectRows()
					_ = c.Global()
				}
			}
		}(i)
	}
	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}
