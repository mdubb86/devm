package serviceapi

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withNoProxyListenerRetryDelay swaps the retry sleep for an instant
// no-op for the duration of the test, so the 500ms production delay
// doesn't slow the suite down.
func withNoProxyListenerRetryDelay(t *testing.T) {
	t.Helper()
	orig := proxyListenerSleep
	proxyListenerSleep = func(time.Duration) {}
	t.Cleanup(func() { proxyListenerSleep = orig })
}

func TestProxyListenerCheck_HealthyReturnsNoDrift(t *testing.T) {
	withNoProxyListenerRetryDelay(t)
	cache := NewStateCache()
	cache.SetVMState("proj-a", VMRunning)
	respawnCalls := 0
	fake := &fakeGroundTruth{
		Projects: []string{"proj-a"},
		ProxyListenerHealthFn: func(ctx context.Context, projectID string) bool {
			return true
		},
		RespawnProxyListenersFn: func(ctx context.Context, projectID string) error {
			respawnCalls++
			return nil
		},
	}
	check := NewProxyListenerCheck()
	drifted, err := check.Run(context.Background(), cache, fake)
	require.NoError(t, err)
	assert.False(t, drifted)
	assert.Equal(t, 0, respawnCalls)
	row, _ := cache.ProjectRow("proj-a")
	assert.True(t, row.ProxyListenerHealth)
}

func TestProxyListenerCheck_UnhealthyTriggersRespawn(t *testing.T) {
	withNoProxyListenerRetryDelay(t)
	cache := NewStateCache()
	cache.SetVMState("proj-a", VMRunning)
	respawnCalls := 0
	fake := &fakeGroundTruth{
		Projects: []string{"proj-a"},
		ProxyListenerHealthFn: func(ctx context.Context, projectID string) bool {
			return false
		},
		RespawnProxyListenersFn: func(ctx context.Context, projectID string) error {
			respawnCalls++
			return nil
		},
	}
	check := NewProxyListenerCheck()
	drifted, err := check.Run(context.Background(), cache, fake)
	require.NoError(t, err)
	assert.True(t, drifted)
	assert.Equal(t, 1, respawnCalls)
	row, _ := cache.ProjectRow("proj-a")
	assert.False(t, row.ProxyListenerHealth)
}

func TestProxyListenerCheck_TransportBlipRetryTolerant(t *testing.T) {
	withNoProxyListenerRetryDelay(t)
	cache := NewStateCache()
	cache.SetVMState("proj-a", VMRunning)
	calls := 0
	respawnCalls := 0
	fake := &fakeGroundTruth{
		Projects: []string{"proj-a"},
		ProxyListenerHealthFn: func(ctx context.Context, projectID string) bool {
			calls++
			return calls > 1
		},
		RespawnProxyListenersFn: func(ctx context.Context, projectID string) error {
			respawnCalls++
			return nil
		},
	}
	check := NewProxyListenerCheck()
	drifted, err := check.Run(context.Background(), cache, fake)
	require.NoError(t, err)
	assert.False(t, drifted, "one bad probe followed by a good probe must not trigger respawn")
	assert.Equal(t, 2, calls, "expected a retry probe after the first failure")
	assert.Equal(t, 0, respawnCalls)
	row, _ := cache.ProjectRow("proj-a")
	assert.True(t, row.ProxyListenerHealth, "final (post-retry) result must be recorded")
}

func TestProxyListenerCheck_SkipsNonRunningProjects(t *testing.T) {
	withNoProxyListenerRetryDelay(t)
	cache := NewStateCache()
	cache.SetVMState("proj-running", VMRunning)
	cache.SetVMState("proj-stopped", VMStopped)
	probed := []string{}
	fake := &fakeGroundTruth{
		Projects: []string{"proj-running", "proj-stopped"},
		ProxyListenerHealthFn: func(ctx context.Context, projectID string) bool {
			probed = append(probed, projectID)
			return true
		},
	}
	check := NewProxyListenerCheck()
	drifted, err := check.Run(context.Background(), cache, fake)
	require.NoError(t, err)
	assert.False(t, drifted)
	assert.Equal(t, []string{"proj-running"}, probed)
	stoppedRow, _ := cache.ProjectRow("proj-stopped")
	assert.False(t, stoppedRow.ProxyListenerHealth, "non-running project must not be probed or updated")
}

func TestProxyListenerCheck_RespawnFollowedByHealthProbeUpdatesCache(t *testing.T) {
	withNoProxyListenerRetryDelay(t)
	cache := NewStateCache()
	cache.SetVMState("proj-a", VMRunning)
	calls := 0
	respawnCalls := 0
	fake := &fakeGroundTruth{
		Projects: []string{"proj-a"},
		ProxyListenerHealthFn: func(ctx context.Context, projectID string) bool {
			calls++
			// 1: initial probe, 2: retry probe — both unhealthy.
			// 3: post-respawn re-probe — healthy, listener self-healed.
			return calls >= 3
		},
		RespawnProxyListenersFn: func(ctx context.Context, projectID string) error {
			respawnCalls++
			return nil
		},
	}
	check := NewProxyListenerCheck()
	drifted, err := check.Run(context.Background(), cache, fake)
	require.NoError(t, err)
	assert.True(t, drifted, "a respawn happened this tick, so drift is still reported")
	assert.Equal(t, 1, respawnCalls)
	assert.Equal(t, 3, calls, "expected initial probe, retry probe, and post-respawn re-probe")
	row, _ := cache.ProjectRow("proj-a")
	assert.True(t, row.ProxyListenerHealth, "cache must reflect the post-respawn healthy re-probe")
}

func TestProxyListenerCheck_RespawnFailureReturnsErrorButCacheReflectsObserved(t *testing.T) {
	withNoProxyListenerRetryDelay(t)
	cache := NewStateCache()
	cache.SetVMState("proj-a", VMRunning)
	fake := &fakeGroundTruth{
		Projects: []string{"proj-a"},
		ProxyListenerHealthFn: func(ctx context.Context, projectID string) bool {
			return false
		},
		RespawnProxyListenersFn: func(ctx context.Context, projectID string) error {
			return errRespawnFailed
		},
	}
	check := NewProxyListenerCheck()
	drifted, err := check.Run(context.Background(), cache, fake)
	assert.True(t, drifted)
	require.Error(t, err)
	row, _ := cache.ProjectRow("proj-a")
	assert.False(t, row.ProxyListenerHealth, "cache must reflect observed reality even when repair failed")
}
