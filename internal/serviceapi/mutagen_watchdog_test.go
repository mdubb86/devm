package serviceapi

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/supervisor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHealMutagen_MissingSpawnsFresh verifies the watchdog respawns the
// mutagen daemon when the supervisor has no entry for it.
func TestHealMutagen_MissingSpawnsFresh(t *testing.T) {
	sup := supervisor.New(t.TempDir())

	origSpawn := spawnMutagenFn
	t.Cleanup(func() { spawnMutagenFn = origSpawn })
	var spawnCalls int
	spawnMutagenFn = func(_ context.Context, _ identity.Config, s *supervisor.Supervisor) error {
		spawnCalls++
		// Mark the daemon present so a re-check under the same key
		// would observe success, mirroring the real SpawnMutagen's
		// effect of adopting the new PID.
		s.Adopt(supervisor.Key{Role: supervisor.RoleMutagen}, os.Getpid())
		return nil
	}

	err := healMutagen(context.Background(), identity.Prod, sup)

	require.NoError(t, err)
	assert.Equal(t, 1, spawnCalls, "missing mutagen daemon should be respawned")
}

// TestHealMutagen_PresentSkipsSpawn verifies a healthy mutagen daemon is
// left alone.
func TestHealMutagen_PresentSkipsSpawn(t *testing.T) {
	sup := supervisor.New(t.TempDir())
	sup.Adopt(supervisor.Key{Role: supervisor.RoleMutagen}, os.Getpid())

	origSpawn := spawnMutagenFn
	t.Cleanup(func() { spawnMutagenFn = origSpawn })
	var spawnCalls int
	spawnMutagenFn = func(_ context.Context, _ identity.Config, _ *supervisor.Supervisor) error {
		spawnCalls++
		return nil
	}

	err := healMutagen(context.Background(), identity.Prod, sup)

	require.NoError(t, err)
	assert.Equal(t, 0, spawnCalls, "present mutagen daemon should not be respawned")
}

// TestHealMutagen_SpawnErrorPropagates verifies a respawn failure is
// surfaced to the caller (the watchdog loop logs it and retries next
// tick) rather than swallowed.
func TestHealMutagen_SpawnErrorPropagates(t *testing.T) {
	sup := supervisor.New(t.TempDir())

	origSpawn := spawnMutagenFn
	t.Cleanup(func() { spawnMutagenFn = origSpawn })
	wantErr := errors.New("boom")
	spawnMutagenFn = func(_ context.Context, _ identity.Config, _ *supervisor.Supervisor) error {
		return wantErr
	}

	err := healMutagen(context.Background(), identity.Prod, sup)
	assert.ErrorIs(t, err, wantErr)
}

// TestRunMutagenWatchdog_ExitsOnCancel drives the blocking poll loop
// directly (no 30s ticks involved) and verifies ctx cancellation makes
// it return promptly.
func TestRunMutagenWatchdog_ExitsOnCancel(t *testing.T) {
	sup := supervisor.New(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- runMutagenWatchdog(ctx, identity.Prod, sup) }()

	cancel()

	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("runMutagenWatchdog did not exit after ctx cancel")
	}
}

// TestStartMutagenWatchdog_ReturnsImmediately is a smoke test for the
// public entrypoint: it must launch the poll loop in the background and
// return to the caller without blocking, regardless of the 30s tick
// interval.
func TestStartMutagenWatchdog_ReturnsImmediately(t *testing.T) {
	sup := supervisor.New(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		StartMutagenWatchdog(ctx, identity.Prod, sup)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("StartMutagenWatchdog blocked instead of returning immediately")
	}
}
