package serviceapi

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBundleDriftCatchup_RefreshesDriftedRunningProject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := identity.Prod
	cache := NewStateCache()
	cache.SetBuild(Build{Fingerprint: "new-fp"})

	require.NoError(t, WriteStateSnapshot(cfg, "proj", StateSnapshot{
		Cfg:               schema.Config{Project: schema.Project{Name: "proj"}},
		BundleFingerprint: "old-fp",
	}))
	cache.SetVMState("proj", VMRunning)

	tr := fakeExecStdinTart(t)

	BundleDriftCatchup(cfg, cache, tr)

	stored, err := ReadStateSnapshot(cfg, "proj")
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, "new-fp", stored.BundleFingerprint, "catchup must refresh drifted project")
}

func TestBundleDriftCatchup_SkipsStoppedProject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := identity.Prod
	cache := NewStateCache()
	cache.SetBuild(Build{Fingerprint: "new-fp"})

	require.NoError(t, WriteStateSnapshot(cfg, "proj", StateSnapshot{
		Cfg:               schema.Config{Project: schema.Project{Name: "proj"}},
		BundleFingerprint: "old-fp",
	}))
	cache.SetVMState("proj", VMStopped)

	// tart binary that would fail if called — proves catchup skipped this project.
	bin := filepath.Join(t.TempDir(), "tart-must-not-run")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nexit 42\n"), 0o755))
	tr := tart.New()
	tr.Path = bin

	BundleDriftCatchup(cfg, cache, tr)

	stored, err := ReadStateSnapshot(cfg, "proj")
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, "old-fp", stored.BundleFingerprint, "stopped project must not be touched")
}

func TestBundleDriftCatchup_SkipsMatchingFingerprint(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := identity.Prod
	cache := NewStateCache()
	cache.SetBuild(Build{Fingerprint: "same-fp"})

	require.NoError(t, WriteStateSnapshot(cfg, "proj", StateSnapshot{
		Cfg:               schema.Config{Project: schema.Project{Name: "proj"}},
		BundleFingerprint: "same-fp",
	}))
	cache.SetVMState("proj", VMRunning)

	bin := filepath.Join(t.TempDir(), "tart-must-not-run")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nexit 42\n"), 0o755))
	tr := tart.New()
	tr.Path = bin

	BundleDriftCatchup(cfg, cache, tr)

	stored, err := ReadStateSnapshot(cfg, "proj")
	require.NoError(t, err)
	assert.Equal(t, "same-fp", stored.BundleFingerprint)
}

// TestBundleDriftCatchup_HungGuestTimesOutAndSweepContinues proves the
// per-project timeout guard fires: a wedged guest agent on project A
// is skipped after bundleCatchupPerProjectTimeout so project B still
// gets refreshed and the sweep returns in bounded time. Without the
// guard, tart.ExecStdin against a hung guest would block the entire
// startup path (see refresh_bundle.go's ctx.Err() branch).
func TestBundleDriftCatchup_HungGuestTimesOutAndSweepContinues(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := identity.Prod
	cache := NewStateCache()
	cache.SetBuild(Build{Fingerprint: "new-fp"})

	require.NoError(t, WriteStateSnapshot(cfg, "wedged", StateSnapshot{
		Cfg:               schema.Config{Project: schema.Project{Name: "wedged"}},
		BundleFingerprint: "old-fp",
	}))
	cache.SetVMState("wedged", VMRunning)

	// A tart stand-in that sleeps forever, ignoring stdin — models a
	// guest agent that never returns. `sleep 300 & wait` shape so
	// SIGKILL from exec.CommandContext ends the process on ctx expiry.
	bin := filepath.Join(t.TempDir(), "tart-hang")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nsleep 300\n"), 0o755))
	tr := tart.New()
	tr.Path = bin

	// Shorten so the test doesn't wait a real minute.
	prev := bundleCatchupPerProjectTimeout
	bundleCatchupPerProjectTimeout = 200 * time.Millisecond
	t.Cleanup(func() { bundleCatchupPerProjectTimeout = prev })

	done := make(chan struct{})
	start := time.Now()
	go func() {
		BundleDriftCatchup(cfg, cache, tr)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("BundleDriftCatchup blocked past the per-project timeout — the guard is not firing")
	}
	if elapsed := time.Since(start); elapsed < 200*time.Millisecond {
		t.Fatalf("sweep returned before the timeout elapsed (%s) — test isn't exercising the timeout path", elapsed)
	}

	// The wedged project's snapshot must NOT have been stamped —
	// tart never succeeded, so nothing to record.
	stored, err := ReadStateSnapshot(cfg, "wedged")
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, "old-fp", stored.BundleFingerprint,
		"hung project must not have its fingerprint stamped as refreshed")
}

func TestBundleDriftCatchup_MalformedSnapshotSkipped(t *testing.T) {
	// A project whose state json is corrupt must not block the
	// sweep — log, skip, continue.
	t.Setenv("HOME", t.TempDir())
	cfg := identity.Prod
	cache := NewStateCache()
	cache.SetBuild(Build{Fingerprint: "new-fp"})
	cache.SetVMState("broken", VMRunning)

	stateDir := filepath.Join(cfg.RuntimeDir(), "state")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "broken.json"), []byte("not json"), 0o644))

	tr := fakeExecStdinTart(t)

	require.NotPanics(t, func() { BundleDriftCatchup(cfg, cache, tr) })
}
