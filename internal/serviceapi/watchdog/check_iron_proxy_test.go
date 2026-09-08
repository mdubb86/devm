package watchdog

import (
	"context"
	"errors"
	"testing"

	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIronProxyCheck_NoDrift_TouchesReconciled(t *testing.T) {
	cache := serviceapi.NewStateCache()
	cache.SetIronProxyHealth("p", serviceapi.ProxyHealth{Status: serviceapi.ProxyOK})
	fake := &fakeGroundTruth{
		Projects: []string{"p"},
		IronProxyHealthFn: func(ctx context.Context, projectID string) serviceapi.ProxyHealth {
			return serviceapi.ProxyHealth{Status: serviceapi.ProxyOK}
		},
	}
	check := NewIronProxyCheck()
	drifted, err := check.Run(context.Background(), cache, fake)
	require.NoError(t, err)
	assert.False(t, drifted)
	row, _ := cache.ProjectRow("p")
	assert.False(t, row.LastReconciledAt.IsZero())
}

func TestIronProxyCheck_DriftMissing_RespawnedAndCacheUpdated(t *testing.T) {
	cache := serviceapi.NewStateCache()
	cache.SetIronProxyHealth("p", serviceapi.ProxyHealth{Status: serviceapi.ProxyOK})
	respawned := false
	calls := 0
	fake := &fakeGroundTruth{
		Projects: []string{"p"},
		IronProxyHealthFn: func(ctx context.Context, projectID string) serviceapi.ProxyHealth {
			calls++
			if calls == 1 {
				return serviceapi.ProxyHealth{Status: serviceapi.ProxyMissing}
			}
			return serviceapi.ProxyHealth{Status: serviceapi.ProxyOK}
		},
		RespawnIronFn: func(ctx context.Context, projectID string) error {
			respawned = true
			return nil
		},
	}
	check := NewIronProxyCheck()
	drifted, err := check.Run(context.Background(), cache, fake)
	require.NoError(t, err)
	assert.True(t, drifted)
	assert.True(t, respawned)
	assert.Equal(t, 2, calls, "expected re-observe after respawn")
	row, _ := cache.ProjectRow("p")
	assert.Equal(t, serviceapi.ProxyOK, row.IronProxyHealth.Status)
}

func TestIronProxyCheck_DriftRepairFailed_CacheReflectsObservedAndErrors(t *testing.T) {
	cache := serviceapi.NewStateCache()
	cache.SetIronProxyHealth("p", serviceapi.ProxyHealth{Status: serviceapi.ProxyOK})
	fake := &fakeGroundTruth{
		Projects: []string{"p"},
		IronProxyHealthFn: func(ctx context.Context, projectID string) serviceapi.ProxyHealth {
			return serviceapi.ProxyHealth{Status: serviceapi.ProxyMissing}
		},
		RespawnIronFn: func(ctx context.Context, projectID string) error {
			return errRespawnFailed
		},
	}
	check := NewIronProxyCheck()
	drifted, err := check.Run(context.Background(), cache, fake)
	assert.True(t, drifted)
	require.Error(t, err)
	row, _ := cache.ProjectRow("p")
	assert.Equal(t, serviceapi.ProxyMissing, row.IronProxyHealth.Status,
		"cache must reflect observed reality even when repair failed")
}

var errRespawnFailed = errors.New("respawn failed for test")
