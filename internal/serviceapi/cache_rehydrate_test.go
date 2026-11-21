package serviceapi

import (
	"testing"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRehydrateCacheFromStateSnapshots_LoadsMacCwd pins that a
// running project's MacCwd is loaded from its persisted StateSnapshot
// into the fresh cache on daemon-restart adopt. Without this, the
// propose/passthrough/approve handlers reject every guest-initiated
// call with 412 "project not started" until the user re-runs
// `devm start` — silently-broken adopt.
func TestRehydrateCacheFromStateSnapshots_LoadsMacCwd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := identity.Prod

	require.NoError(t, WriteStateSnapshot(cfg, "proj", StateSnapshot{
		Cfg:    schema.Config{Project: schema.Project{Name: "proj"}},
		MacCwd: "/Users/dev/project",
	}))

	cache := NewStateCache()
	rehydrateCacheFromStateSnapshots(cfg, cache, []string{"proj"})

	row, ok := cache.ProjectRow("proj")
	require.True(t, ok)
	assert.Equal(t, "/Users/dev/project", row.MacCwd,
		"adopt rehydrate must load MacCwd from StateSnapshot into the fresh cache")
}

// TestRehydrateCacheFromStateSnapshots_MissingSnapshotSkips pins that
// a project whose state snapshot is missing (never fully cold-started,
// or hand-cleaned) doesn't break the rehydrate for other projects and
// doesn't cause the daemon to fail startup.
func TestRehydrateCacheFromStateSnapshots_MissingSnapshotSkips(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := identity.Prod

	require.NoError(t, WriteStateSnapshot(cfg, "good", StateSnapshot{
		Cfg:    schema.Config{Project: schema.Project{Name: "good"}},
		MacCwd: "/Users/dev/good",
	}))
	// "orphan" has no snapshot on disk.

	cache := NewStateCache()
	rehydrateCacheFromStateSnapshots(cfg, cache, []string{"orphan", "good"})

	goodRow, ok := cache.ProjectRow("good")
	require.True(t, ok)
	assert.Equal(t, "/Users/dev/good", goodRow.MacCwd)

	// orphan gets a zero row; MacCwd remains empty. The daemon didn't
	// crash on the missing file.
	orphanRow, _ := cache.ProjectRow("orphan")
	assert.Empty(t, orphanRow.MacCwd)
}

// TestRehydrateCacheFromStateSnapshots_EmptyMacCwdSkipsSet pins that a
// snapshot with an empty MacCwd (e.g. written before the field was
// added, or by a code path that didn't stamp it) doesn't call
// SetMacCwd with an empty string — that would overwrite anything a
// subsequent /vm/start put there.
func TestRehydrateCacheFromStateSnapshots_EmptyMacCwdSkipsSet(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := identity.Prod

	require.NoError(t, WriteStateSnapshot(cfg, "proj", StateSnapshot{
		Cfg:    schema.Config{Project: schema.Project{Name: "proj"}},
		MacCwd: "", // deliberately empty
	}))
	cache := NewStateCache()
	// Seed a value to prove rehydrate won't clobber it with the empty
	// on-disk value.
	cache.SetMacCwd("proj", "/preexisting/value")

	rehydrateCacheFromStateSnapshots(cfg, cache, []string{"proj"})

	row, _ := cache.ProjectRow("proj")
	assert.Equal(t, "/preexisting/value", row.MacCwd,
		"rehydrate must not clobber existing cache MacCwd with an empty snapshot value")
}
