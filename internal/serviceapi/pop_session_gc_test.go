package serviceapi

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPopSessionTTL_DefaultAndEnv(t *testing.T) {
	t.Setenv("DEVM_POP_SESSION_TTL_SECONDS", "")
	assert.Equal(t, time.Hour, PopSessionTTL())

	t.Setenv("DEVM_POP_SESSION_TTL_SECONDS", "5")
	assert.Equal(t, 5*time.Second, PopSessionTTL())
}

func TestPopSessionGCInterval_DefaultAndEnv(t *testing.T) {
	t.Setenv("DEVM_POP_SESSION_GC_INTERVAL_SECONDS", "")
	assert.Equal(t, 5*time.Minute, PopSessionGCInterval())

	t.Setenv("DEVM_POP_SESSION_GC_INTERVAL_SECONDS", "1")
	assert.Equal(t, time.Second, PopSessionGCInterval())
}

func TestGCPopSessionsOnce_SessionPastTTL_TornDown(t *testing.T) {
	cfg := testPopSessionCfg(t)

	scripted := &scriptedCLI{}
	cli := scripted.build()
	store := NewPopSessionStore()

	ps, _, err := store.GetOrCreate(cfg, "p", "/tmp/x", PopKindFile, func(ps *PopSession) error {
		return CreatePopSyncSession(cli, cfg, "devm-p", ps)
	})
	require.NoError(t, err)

	// The mutagen fake never writes into MacDir, so it stays empty — GC
	// falls back to MacDir's own mtime as LastChangeAt (see note 1 in the
	// task brief). Backdating MacDir alone is sufficient here.
	twoHoursAgo := time.Now().Add(-2 * time.Hour)
	require.NoError(t, os.Chtimes(ps.MacDir, twoHoursAgo, twoHoursAgo))

	removed := GCPopSessionsOnce(store, cli, cfg, time.Hour, time.Now)
	require.Len(t, removed, 1)
	assert.Equal(t, ps.ID, removed[0].ID)
	_, err = os.Stat(ps.MacDir)
	assert.True(t, os.IsNotExist(err))
	assert.Empty(t, store.All())

	// sync terminate was invoked for the gc'd session.
	require.NotEmpty(t, scripted.terminateCall)
	assert.Equal(t, ps.MutagenSessionID, scripted.terminateCall[len(scripted.terminateCall)-1])
}

func TestGCPopSessionsOnce_SessionWithinTTL_Survives(t *testing.T) {
	cfg := testPopSessionCfg(t)

	scripted := &scriptedCLI{}
	cli := scripted.build()
	store := NewPopSessionStore()

	_, _, err := store.GetOrCreate(cfg, "p", "/tmp/y", PopKindFile, func(ps *PopSession) error {
		return CreatePopSyncSession(cli, cfg, "devm-p", ps)
	})
	require.NoError(t, err)

	removed := GCPopSessionsOnce(store, cli, cfg, time.Hour, time.Now)
	assert.Empty(t, removed)
	assert.Len(t, store.All(), 1)
}

func TestSweepProjectPopSessions_TearsDownAllForProject(t *testing.T) {
	cfg := testPopSessionCfg(t)

	scripted := &scriptedCLI{}
	cli := scripted.build()
	store := NewPopSessionStore()

	for _, gp := range []string{"/tmp/a", "/tmp/b"} {
		_, _, err := store.GetOrCreate(cfg, "p", gp, PopKindFile, func(ps *PopSession) error {
			return CreatePopSyncSession(cli, cfg, "devm-p", ps)
		})
		require.NoError(t, err)
	}
	_, _, err := store.GetOrCreate(cfg, "q", "/tmp/c", PopKindFile, func(ps *PopSession) error {
		return CreatePopSyncSession(cli, cfg, "devm-q", ps)
	})
	require.NoError(t, err)

	SweepProjectPopSessions(store, cli, cfg, "p")
	remaining := store.All()
	require.Len(t, remaining, 1)
	assert.Equal(t, "q", remaining[0].ProjectName)
}

func TestSweepAllPopSessions_TearsDownEverySession(t *testing.T) {
	cfg := testPopSessionCfg(t)

	scripted := &scriptedCLI{}
	cli := scripted.build()
	store := NewPopSessionStore()

	for _, gp := range []string{"/tmp/a", "/tmp/b"} {
		_, _, err := store.GetOrCreate(cfg, "p", gp, PopKindFile, func(ps *PopSession) error {
			return CreatePopSyncSession(cli, cfg, "devm-p", ps)
		})
		require.NoError(t, err)
	}

	SweepAllPopSessions(store, cli, cfg)
	assert.Empty(t, store.All())
}

func TestGCPopSessionsOnce_AlreadyRemoved_SkipsTeardown(t *testing.T) {
	// Simulate a race between GC and another sweep: the session is
	// removed from the store before GC gets to call TearDown. RemoveByID
	// returning nil must short-circuit — never a double sync terminate.
	cfg := testPopSessionCfg(t)

	scripted := &scriptedCLI{}
	cli := scripted.build()
	store := NewPopSessionStore()

	ps, _, err := store.GetOrCreate(cfg, "p", "/tmp/x", PopKindFile, func(ps *PopSession) error {
		return CreatePopSyncSession(cli, cfg, "devm-p", ps)
	})
	require.NoError(t, err)

	twoHoursAgo := time.Now().Add(-2 * time.Hour)
	require.NoError(t, os.Chtimes(ps.MacDir, twoHoursAgo, twoHoursAgo))

	// Win the race ourselves before GC runs.
	require.NotNil(t, store.RemoveByID(ps.ID))

	removed := GCPopSessionsOnce(store, cli, cfg, time.Hour, time.Now)
	assert.Empty(t, removed, "already-removed session must not be reported as gc'd")
	assert.Empty(t, scripted.terminateCall, "must not double-terminate a session someone else already removed")
}

func TestRunPopSessionGC_TicksAndSweepsUntilCancel(t *testing.T) {
	cfg := testPopSessionCfg(t)

	scripted := &scriptedCLI{}
	cli := scripted.build()
	store := NewPopSessionStore()

	ps, _, err := store.GetOrCreate(cfg, "p", "/tmp/x", PopKindFile, func(ps *PopSession) error {
		return CreatePopSyncSession(cli, cfg, "devm-p", ps)
	})
	require.NoError(t, err)
	twoHoursAgo := time.Now().Add(-2 * time.Hour)
	require.NoError(t, os.Chtimes(ps.MacDir, twoHoursAgo, twoHoursAgo))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunPopSessionGC(ctx, store, cli, cfg, time.Hour, time.Millisecond)
	}()

	require.Eventually(t, func() bool {
		return len(store.All()) == 0
	}, time.Second, time.Millisecond, "GC loop must sweep the stale session within a few ticks")

	cancel()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("RunPopSessionGC did not return after ctx cancel")
	}
}

func TestWipePopScratchOnStartup_RemovesDir(t *testing.T) {
	cfg := testPopSessionCfg(t)

	root := PopScratchRoot(cfg)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "leftover-id"), 0755))
	require.NoError(t, WipePopScratchOnStartup(cfg))
	_, err := os.Stat(root)
	assert.True(t, os.IsNotExist(err))

	// Idempotent.
	require.NoError(t, WipePopScratchOnStartup(cfg))
}
