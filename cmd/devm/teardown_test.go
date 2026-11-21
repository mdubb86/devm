package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mdubb86/devm/internal/orchestrator"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTeardownCmd_SkipSnapshotFlag pins that `devm teardown
// --skip-snapshot` threads the flag into StopDeps so the orchestrator
// bypasses the on_teardown lifecycle snapshot block. `--yes` is
// required because the command refuses to proceed non-interactively
// without it.
func TestTeardownCmd_SkipSnapshotFlag(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "devm.yaml"),
		[]byte("project:\n  name: p\n"), 0o644))
	stubResolveProjectFn(t, "p", dir)

	orig := runStopFn
	t.Cleanup(func() { runStopFn = orig })
	var captured orchestrator.StopDeps
	var capturedMode orchestrator.Destructiveness
	runStopFn = func(_ context.Context, d orchestrator.StopDeps, _ string, mode orchestrator.Destructiveness) (int, error) {
		captured = d
		capturedMode = mode
		return 0, nil
	}

	t.Cleanup(func() {
		teardownSkipSnapshot = false
		teardownYes = false
	})

	teardownCmd.SetContext(context.Background())
	require.NoError(t, teardownCmd.ParseFlags([]string{"--yes", "--skip-snapshot"}))
	require.NoError(t, teardownCmd.RunE(teardownCmd, nil))

	assert.True(t, captured.SkipSnapshot, "--skip-snapshot must set StopDeps.SkipSnapshot")
	assert.Equal(t, orchestrator.StopDestroy, capturedMode)
	assert.Equal(t, "p", captured.Cfg.Project.Name, "StopDeps.Cfg must carry the loaded project config")
}

// TestTeardownCmd_SkipSnapshotDefaultsFalse pins that omitting the
// flag leaves SkipSnapshot at its zero value.
func TestTeardownCmd_SkipSnapshotDefaultsFalse(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "devm.yaml"),
		[]byte("project:\n  name: p\n"), 0o644))
	stubResolveProjectFn(t, "p", dir)

	orig := runStopFn
	t.Cleanup(func() { runStopFn = orig })
	var captured orchestrator.StopDeps
	runStopFn = func(_ context.Context, d orchestrator.StopDeps, _ string, _ orchestrator.Destructiveness) (int, error) {
		captured = d
		return 0, nil
	}

	t.Cleanup(func() {
		teardownSkipSnapshot = false
		teardownYes = false
	})
	teardownSkipSnapshot = false

	teardownCmd.SetContext(context.Background())
	require.NoError(t, teardownCmd.ParseFlags([]string{"--yes"}))
	require.NoError(t, teardownCmd.RunE(teardownCmd, nil))

	assert.False(t, captured.SkipSnapshot, "default must be false")
}
