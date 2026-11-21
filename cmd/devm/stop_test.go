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

// TestStopCmd_SkipSnapshotFlag pins that `devm stop --skip-snapshot`
// threads the flag into StopDeps so the orchestrator bypasses the
// on_stop lifecycle snapshot block.
func TestStopCmd_SkipSnapshotFlag(t *testing.T) {
	// Isolate the daemon handshake call: no daemon listens under this
	// HOME, which daemonHandshake tolerates as a nil-error no-op.
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

	// Reset the flag to false after the test so subsequent tests see a
	// clean state — cobra's BoolVar binds a package global.
	t.Cleanup(func() { stopSkipSnapshot = false })

	stopCmd.SetContext(context.Background())
	require.NoError(t, stopCmd.ParseFlags([]string{"--skip-snapshot"}))
	require.NoError(t, stopCmd.RunE(stopCmd, nil))

	assert.True(t, captured.SkipSnapshot, "--skip-snapshot must set StopDeps.SkipSnapshot")
	assert.Equal(t, orchestrator.StopPreserve, capturedMode)
	assert.Equal(t, "p", captured.Cfg.Project.Name, "StopDeps.Cfg must carry the loaded project config")
}

// TestStopCmd_SkipSnapshotDefaultsFalse pins that omitting the flag
// leaves SkipSnapshot at its zero value — the on_stop block runs by
// default.
func TestStopCmd_SkipSnapshotDefaultsFalse(t *testing.T) {
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

	t.Cleanup(func() { stopSkipSnapshot = false })
	stopSkipSnapshot = false // paranoia — in case a prior test leaked a value

	stopCmd.SetContext(context.Background())
	require.NoError(t, stopCmd.ParseFlags([]string{}))
	require.NoError(t, stopCmd.RunE(stopCmd, nil))

	assert.False(t, captured.SkipSnapshot, "default must be false")
}
