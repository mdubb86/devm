package serviceapi

import (
	"os"
	"path/filepath"
	"testing"

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
