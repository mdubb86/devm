package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------- fakes for RunStop tests ----------

// fakeStopClient records StopVM calls and returns a scripted error.
type fakeStopClient struct {
	stopCalled int
	stopErr    error
}

func (f *fakeStopClient) StopVM(_ context.Context, _ string) error {
	f.stopCalled++
	return f.stopErr
}

// ---------- RunStop tests ----------

func TestRunStopPreserve_CallsStopVM(t *testing.T) {
	repoRoot := t.TempDir()
	admin := &fakeStopClient{}
	in := strings.NewReader("y\n")
	out := &bytes.Buffer{}

	deps := StopDeps{
		Tart:             tartPathNotNeeded(t),
		ServiceAPIClient: admin,
		LockPath:         filepath.Join(repoRoot, ".devm/lock"),
		In:               in,
		Out:              out,
	}
	rc, err := RunStop(context.Background(), deps, "proj-123", "proj-sbx", StopPreserve, false)
	require.NoError(t, err)
	assert.Equal(t, 0, rc)
	assert.Equal(t, 1, admin.stopCalled, "StopVM must be called once")
	assert.Contains(t, out.String(), "Stopped VM proj-sbx")
	assert.Contains(t, out.String(), "Disk preserved")
}

func TestRunStopDestroy_CallsStopVMThenDeletesDisk(t *testing.T) {
	repoRoot := t.TempDir()
	admin := &fakeStopClient{}

	// fakeTartBin from shell_test.go: exits 0 for all subcommands.
	tr := fakeTartBin(t, repoRoot)
	in := strings.NewReader("y\n")
	out := &bytes.Buffer{}

	deps := StopDeps{
		Tart:             tr,
		ServiceAPIClient: admin,
		LockPath:         filepath.Join(repoRoot, ".devm/lock"),
		In:               in,
		Out:              out,
	}
	rc, err := RunStop(context.Background(), deps, "proj-123", "proj-sbx", StopDestroy, false)
	require.NoError(t, err)
	assert.Equal(t, 0, rc)
	assert.Equal(t, 1, admin.stopCalled, "StopVM must be called before disk delete")
	assert.Contains(t, out.String(), "Deleted VM proj-sbx")
}

func TestRunStopRefusalWithNo(t *testing.T) {
	repoRoot := t.TempDir()
	admin := &fakeStopClient{}
	in := strings.NewReader("n\n")
	out := &bytes.Buffer{}

	deps := StopDeps{
		Tart:             tartPathNotNeeded(t),
		ServiceAPIClient: admin,
		LockPath:         filepath.Join(repoRoot, ".devm/lock"),
		In:               in,
		Out:              out,
	}
	rc, err := RunStop(context.Background(), deps, "proj-123", "proj-sbx", StopPreserve, false)
	require.NoError(t, err)
	assert.Equal(t, 1, rc, "refusal exits 1")
	assert.Equal(t, 0, admin.stopCalled, "StopVM must not be called after refusal")
	assert.Contains(t, out.String(), "aborted")
	assert.Contains(t, out.String(), "[y/N]")
}

func TestRunStopAutoApproveSkipsPrompt(t *testing.T) {
	repoRoot := t.TempDir()
	admin := &fakeStopClient{}
	in := strings.NewReader("") // nothing to read
	out := &bytes.Buffer{}

	deps := StopDeps{
		Tart:             tartPathNotNeeded(t),
		ServiceAPIClient: admin,
		LockPath:         filepath.Join(repoRoot, ".devm/lock"),
		In:               in,
		Out:              out,
	}
	rc, err := RunStop(context.Background(), deps, "proj-123", "proj-sbx", StopPreserve, true)
	require.NoError(t, err)
	assert.Equal(t, 0, rc)
	assert.Equal(t, 1, admin.stopCalled)
}

func TestRunStopDaemonFailContinuesForTeardown(t *testing.T) {
	// If the daemon StopVM fails, teardown should still attempt disk deletion.
	repoRoot := t.TempDir()
	admin := &fakeStopClient{stopErr: errors.New("daemon down")}
	tr := fakeTartBin(t, repoRoot)
	out := &bytes.Buffer{}

	deps := StopDeps{
		Tart:             tr,
		ServiceAPIClient: admin,
		LockPath:         filepath.Join(repoRoot, ".devm/lock"),
		Out:              out,
	}
	rc, err := RunStop(context.Background(), deps, "proj-123", "proj-sbx", StopDestroy, true)
	require.NoError(t, err)
	assert.Equal(t, 0, rc)
	assert.Contains(t, out.String(), "daemon down", "daemon error must be noted")
	assert.Contains(t, out.String(), "Deleted VM proj-sbx", "disk delete must still run")
}

func TestRunStopPromptText(t *testing.T) {
	// StopPreserve prompt says "Stop VM"
	admin := &fakeStopClient{}
	inStop := strings.NewReader("n\n")
	outStop := &bytes.Buffer{}
	deps := StopDeps{
		Tart:             tartPathNotNeeded(t),
		ServiceAPIClient: admin,
		LockPath:         filepath.Join(t.TempDir(), ".devm/lock"),
		In:               inStop,
		Out:              outStop,
	}
	_, err := RunStop(context.Background(), deps, "proj-123", "my-vm", StopPreserve, false)
	require.NoError(t, err)
	assert.Contains(t, outStop.String(), "Stop VM my-vm")

	// StopDestroy prompt says "Tear down VM"
	inTear := strings.NewReader("n\n")
	outTear := &bytes.Buffer{}
	deps2 := StopDeps{
		Tart:             tartPathNotNeeded(t),
		ServiceAPIClient: &fakeStopClient{},
		LockPath:         filepath.Join(t.TempDir(), ".devm/lock"),
		In:               inTear,
		Out:              outTear,
	}
	_, err = RunStop(context.Background(), deps2, "proj-123", "my-vm", StopDestroy, false)
	require.NoError(t, err)
	assert.Contains(t, outTear.String(), "Tear down VM my-vm")
}

func TestDestructivenessIdentity(t *testing.T) {
	assert.NotEqual(t, StopPreserve, StopDestroy)
}
