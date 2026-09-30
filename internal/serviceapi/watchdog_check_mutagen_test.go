package serviceapi

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMutagenCheck_NoDrift_TouchesReconciled(t *testing.T) {
	cache := NewStateCache()
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
	cache := NewStateCache()
	cache.SetMutagenDaemonPID(1234)
	cache.SetMutagenHealth("p", MutagenHealth{Status: MutagenOK})
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
	assert.Equal(t, MutagenOK, row.MutagenHealth.Status)
}

// TestMutagenCheck_NoDrift_SeedsMutagenOKWhenEmpty pins the fix for
// I4: during the ~5s gap between daemon boot and the subscriber's
// first tick, devm status --json would report an empty mutagen_health
// field for every project. The mutagen check now seeds MutagenOK on
// the confirmed-alive branch so the field always has a value, and the
// subscriber overwrites with finer-grained state within seconds.
// Only seeds empty rows — a subscriber-written state (MutagenDead or
// otherwise) must not be clobbered.
func TestMutagenCheck_NoDrift_SeedsMutagenOKWhenEmpty(t *testing.T) {
	cache := NewStateCache()
	cache.SetMutagenDaemonPID(1234)
	// One project with no MutagenHealth yet — must be seeded.
	cache.SetMacCwd("empty-proj", "/e")
	// One project the subscriber already reported dead on — must NOT
	// be overwritten by the coarse floor.
	cache.SetMacCwd("dead-proj", "/d")
	cache.SetMutagenHealth("dead-proj", MutagenHealth{Status: MutagenDead})

	fake := &fakeGroundTruth{
		DataDir:      "/tmp/mutagen-data",
		Projects:     []string{"empty-proj", "dead-proj"},
		MutagenPIDFn: func(dataDir string) (int, error) { return 1234, nil },
	}
	check := NewMutagenCheck()
	drifted, err := check.Run(context.Background(), cache, fake)
	require.NoError(t, err)
	assert.False(t, drifted)

	empty, _ := cache.ProjectRow("empty-proj")
	assert.Equal(t, MutagenOK, empty.MutagenHealth.Status,
		"empty MutagenHealth must be seeded MutagenOK when daemon PID is confirmed alive")
	dead, _ := cache.ProjectRow("dead-proj")
	assert.Equal(t, MutagenDead, dead.MutagenHealth.Status,
		"already-set MutagenHealth must NOT be overwritten by the healthy-path seed — subscriber ownership wins")
}

func TestMutagenCheck_RespawnFailed_HealthReflectsDead(t *testing.T) {
	cache := NewStateCache()
	cache.SetMutagenDaemonPID(1234)
	cache.SetMutagenHealth("p", MutagenHealth{Status: MutagenOK})
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
	assert.Equal(t, MutagenDead, row.MutagenHealth.Status)
}
