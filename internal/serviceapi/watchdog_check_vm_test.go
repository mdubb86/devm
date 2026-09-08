package serviceapi

import (
	"context"
	"testing"

	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVMCheck_NoDrift_TouchesReconciled(t *testing.T) {
	cache := NewStateCache()
	cache.SetVMState("p", VMRunning)
	fake := &fakeGroundTruth{
		Projects: []string{"p"},
		TartListFn: func(ctx context.Context) ([]tart.VM, error) {
			return []tart.VM{{Name: "p", Running: true}}, nil
		},
	}
	check := NewVMCheck()
	drifted, err := check.Run(context.Background(), cache, fake)
	require.NoError(t, err)
	assert.False(t, drifted)
}

func TestVMCheck_SilentCrash_CacheReconciles(t *testing.T) {
	cache := NewStateCache()
	cache.SetVMState("p", VMRunning)
	fake := &fakeGroundTruth{
		Projects: []string{"p"},
		TartListFn: func(ctx context.Context) ([]tart.VM, error) {
			// VM died out-of-band; not in list.
			return []tart.VM{}, nil
		},
	}
	check := NewVMCheck()
	drifted, err := check.Run(context.Background(), cache, fake)
	require.NoError(t, err)
	assert.True(t, drifted)
	row, _ := cache.ProjectRow("p")
	assert.Equal(t, VMAbsent, row.VMState)
}

func TestVMCheck_ExternalStop_CacheReconciles(t *testing.T) {
	cache := NewStateCache()
	cache.SetVMState("p", VMRunning)
	fake := &fakeGroundTruth{
		Projects: []string{"p"},
		TartListFn: func(ctx context.Context) ([]tart.VM, error) {
			return []tart.VM{{Name: "p", Running: false}}, nil
		},
	}
	check := NewVMCheck()
	drifted, err := check.Run(context.Background(), cache, fake)
	require.NoError(t, err)
	assert.True(t, drifted)
	row, _ := cache.ProjectRow("p")
	assert.Equal(t, VMStopped, row.VMState)
}

func TestVMCheck_ListError_PropagatesAndSkipsReconcile(t *testing.T) {
	cache := NewStateCache()
	cache.SetVMState("p", VMRunning)
	fake := &fakeGroundTruth{
		Projects: []string{"p"},
		TartListFn: func(ctx context.Context) ([]tart.VM, error) {
			return nil, errRespawnFailed // reuse test-scoped err
		},
	}
	check := NewVMCheck()
	drifted, err := check.Run(context.Background(), cache, fake)
	require.Error(t, err)
	assert.False(t, drifted)
	row, _ := cache.ProjectRow("p")
	assert.Equal(t, VMRunning, row.VMState, "cache unchanged on observe error")
}
