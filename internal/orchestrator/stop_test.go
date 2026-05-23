package orchestrator

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mtwaage/devm/internal/sandbox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunStopPreserveCallsSbxStop(t *testing.T) {
	repoRoot := t.TempDir()
	runner := &stateRunner{lsStatus: "running", probeOut: "27 bash pts/1 agent\n"}
	in := strings.NewReader("y\n")
	out := &bytes.Buffer{}

	deps := StopDeps{
		Runner:   runner,
		LockPath: filepath.Join(repoRoot, ".devm/lock"),
		In:       in,
		Out:      out,
	}
	rc, err := RunStop(context.Background(), deps, "x-sbx", StopPreserve, false)
	require.NoError(t, err)
	assert.Equal(t, 0, rc)

	var sawStop bool
	for _, c := range runner.calls {
		if strings.Join(c, " ") == "sbx stop x-sbx" {
			sawStop = true
		}
	}
	assert.True(t, sawStop, "expected `sbx stop x-sbx` call; got: %v", runner.calls)
}

func TestRunStopDestroyCallsSbxRm(t *testing.T) {
	repoRoot := t.TempDir()
	runner := &stateRunner{lsStatus: "running", probeOut: "27 bash pts/1 agent\n"}
	in := strings.NewReader("y\n")
	out := &bytes.Buffer{}

	deps := StopDeps{
		Runner:   runner,
		LockPath: filepath.Join(repoRoot, ".devm/lock"),
		In:       in,
		Out:      out,
	}
	rc, err := RunStop(context.Background(), deps, "x-sbx", StopDestroy, false)
	require.NoError(t, err)
	assert.Equal(t, 0, rc)

	var sawRm bool
	for _, c := range runner.calls {
		if strings.Join(c, " ") == "sbx rm x-sbx" {
			sawRm = true
		}
	}
	assert.True(t, sawRm, "expected `sbx rm x-sbx` call; got: %v", runner.calls)
}

func TestRunStopRefusalWithNo(t *testing.T) {
	repoRoot := t.TempDir()
	runner := &stateRunner{lsStatus: "running", probeOut: "27 bash pts/1 agent\n"}
	in := strings.NewReader("n\n")
	out := &bytes.Buffer{}

	deps := StopDeps{
		Runner:   runner,
		LockPath: filepath.Join(repoRoot, ".devm/lock"),
		In:       in,
		Out:      out,
	}
	rc, err := RunStop(context.Background(), deps, "x-sbx", StopPreserve, false)
	require.NoError(t, err)
	assert.Equal(t, 1, rc, "refusal exits 1")

	for _, c := range runner.calls {
		joined := strings.Join(c, " ")
		require.False(t, strings.Contains(joined, "sbx stop"), "should not have stopped after refusal: %s", joined)
		require.False(t, strings.Contains(joined, "sbx rm"), "should not have removed after refusal: %s", joined)
	}
}

func TestRunStopAutoApproveSkipsPrompt(t *testing.T) {
	repoRoot := t.TempDir()
	runner := &stateRunner{lsStatus: "running", probeOut: "27 bash pts/1 agent\n"}
	in := strings.NewReader("") // nothing to read
	out := &bytes.Buffer{}

	deps := StopDeps{
		Runner:   runner,
		LockPath: filepath.Join(repoRoot, ".devm/lock"),
		In:       in,
		Out:      out,
	}
	rc, err := RunStop(context.Background(), deps, "x-sbx", StopPreserve, true /* autoApprove */)
	require.NoError(t, err)
	assert.Equal(t, 0, rc)
}

func TestRunStopAlreadyStoppedIsNoOpForPreserve(t *testing.T) {
	repoRoot := t.TempDir()
	runner := &stateRunner{lsStatus: "stopped"}
	out := &bytes.Buffer{}
	deps := StopDeps{
		Runner:   runner,
		LockPath: filepath.Join(repoRoot, ".devm/lock"),
		Out:      out,
	}
	rc, err := RunStop(context.Background(), deps, "x-sbx", StopPreserve, true)
	require.NoError(t, err)
	assert.Equal(t, 0, rc)

	for _, c := range runner.calls {
		require.False(t, strings.Contains(strings.Join(c, " "), "sbx stop"))
	}
}

func TestRunStopAlreadyStoppedStillDestroysForTeardown(t *testing.T) {
	// sbx rm against a stopped sandbox should still proceed.
	repoRoot := t.TempDir()
	runner := &stateRunner{lsStatus: "stopped"}
	out := &bytes.Buffer{}
	deps := StopDeps{
		Runner:   runner,
		LockPath: filepath.Join(repoRoot, ".devm/lock"),
		Out:      out,
	}
	rc, err := RunStop(context.Background(), deps, "x-sbx", StopDestroy, true)
	require.NoError(t, err)
	assert.Equal(t, 0, rc)

	var sawRm bool
	for _, c := range runner.calls {
		if strings.Join(c, " ") == "sbx rm x-sbx" {
			sawRm = true
		}
	}
	assert.True(t, sawRm)
}

func TestDestructivenessIdentity(t *testing.T) {
	assert.NotEqual(t, StopPreserve, StopDestroy)
	var _ sandbox.Runner = &stateRunner{}
}
