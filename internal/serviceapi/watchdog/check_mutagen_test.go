package watchdog

import (
	"context"
	"testing"

	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMutagenCheck_NoDrift_TouchesReconciled(t *testing.T) {
	cache := serviceapi.NewStateCache()
	cache.SetMutagenDaemonPID(1234)
	fake := &fakeGroundTruth{
		DataDir:      "/tmp/mutagen-data",
		Projects:     []string{"p"},
		MutagenPIDFn: func(dataDir string) (int, error) { return 1234, nil },
	}
	check := NewMutagenCheck()
	drifted, err := check.Run(context.Background(), cache, fake)
	require.NoError(t, err)
	assert.False(t, drifted)
	assert.Equal(t, 1234, cache.Global().MutagenDaemonPID)
	assert.False(t, cache.Global().LastReconciledAt.IsZero())
}

func TestMutagenCheck_DaemonDied_RespawnedAndCacheUpdated(t *testing.T) {
	cache := serviceapi.NewStateCache()
	cache.SetMutagenDaemonPID(1234)
	cache.SetMutagenHealth("p", serviceapi.MutagenHealth{Status: serviceapi.MutagenOK})
	respawned := false
	calls := 0
	fake := &fakeGroundTruth{
		DataDir:  "/tmp/mutagen-data",
		Projects: []string{"p"},
		MutagenPIDFn: func(dataDir string) (int, error) {
			calls++
			if calls == 1 {
				return 0, nil // dead
			}
			return 5678, nil // fresh pid after respawn
		},
		RespawnMutagenFn: func(ctx context.Context) error {
			respawned = true
			return nil
		},
	}
	check := NewMutagenCheck()
	drifted, err := check.Run(context.Background(), cache, fake)
	require.NoError(t, err)
	assert.True(t, drifted)
	assert.True(t, respawned)
	assert.Equal(t, 5678, cache.Global().MutagenDaemonPID)
	row, _ := cache.ProjectRow("p")
	assert.Equal(t, serviceapi.MutagenOK, row.MutagenHealth.Status)
}

func TestMutagenCheck_RespawnFailed_HealthReflectsDead(t *testing.T) {
	cache := serviceapi.NewStateCache()
	cache.SetMutagenDaemonPID(1234)
	cache.SetMutagenHealth("p", serviceapi.MutagenHealth{Status: serviceapi.MutagenOK})
	fake := &fakeGroundTruth{
		DataDir:      "/tmp/m",
		Projects:     []string{"p"},
		MutagenPIDFn: func(dataDir string) (int, error) { return 0, nil },
		RespawnMutagenFn: func(ctx context.Context) error {
			return errRespawnFailed
		},
	}
	check := NewMutagenCheck()
	drifted, err := check.Run(context.Background(), cache, fake)
	require.Error(t, err)
	assert.True(t, drifted)
	assert.Equal(t, 0, cache.Global().MutagenDaemonPID)
	row, _ := cache.ProjectRow("p")
	assert.Equal(t, serviceapi.MutagenDead, row.MutagenHealth.Status)
}
