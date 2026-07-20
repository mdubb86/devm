package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/mdubb86/devm/internal/serviceapi/sshkeys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------- fakes for RunStop tests ----------

// fakeStopClient records StopVM calls and returns a scripted error.
type fakeStopClient struct {
	stopCalled int
	stopArgs   []stopCall
	stopErr    error
}

type stopCall struct {
	name string
}

func (f *fakeStopClient) StopVM(_ context.Context, name string) error {
	f.stopCalled++
	f.stopArgs = append(f.stopArgs, stopCall{name: name})
	return f.stopErr
}

// ---------- RunStop tests ----------

func TestRunStopPreserve_CallsStopVM(t *testing.T) {
	admin := &fakeStopClient{}
	in := strings.NewReader("y\n")
	out := &bytes.Buffer{}

	deps := StopDeps{
		Tart:             tartPathNotNeeded(t),
		ServiceAPIClient: admin,
		In:               in,
		Out:              out,
	}
	rc, err := RunStop(context.Background(), deps, "proj-123", StopPreserve, false)
	require.NoError(t, err)
	assert.Equal(t, 0, rc)
	assert.Equal(t, 1, admin.stopCalled, "StopVM must be called once")
	assert.Contains(t, out.String(), "Stopped VM proj-123")
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
		In:               in,
		Out:              out,
	}
	rc, err := RunStop(context.Background(), deps, "proj-123", StopDestroy, false)
	require.NoError(t, err)
	assert.Equal(t, 0, rc)
	assert.Equal(t, 1, admin.stopCalled, "StopVM must be called before disk delete")
	assert.Contains(t, out.String(), "Deleted VM proj-123")
}

func TestRunStopRefusalWithNo(t *testing.T) {
	admin := &fakeStopClient{}
	in := strings.NewReader("n\n")
	out := &bytes.Buffer{}

	deps := StopDeps{
		Tart:             tartPathNotNeeded(t),
		ServiceAPIClient: admin,
		In:               in,
		Out:              out,
	}
	rc, err := RunStop(context.Background(), deps, "proj-123", StopPreserve, false)
	require.NoError(t, err)
	assert.Equal(t, 1, rc, "refusal exits 1")
	assert.Equal(t, 0, admin.stopCalled, "StopVM must not be called after refusal")
	assert.Contains(t, out.String(), "aborted")
	assert.Contains(t, out.String(), "[y/N]")
}

func TestRunStopAutoApproveSkipsPrompt(t *testing.T) {
	admin := &fakeStopClient{}
	in := strings.NewReader("") // nothing to read
	out := &bytes.Buffer{}

	deps := StopDeps{
		Tart:             tartPathNotNeeded(t),
		ServiceAPIClient: admin,
		In:               in,
		Out:              out,
	}
	rc, err := RunStop(context.Background(), deps, "proj-123", StopPreserve, true)
	require.NoError(t, err)
	assert.Equal(t, 0, rc)
	assert.Equal(t, 1, admin.stopCalled)
}

func TestRunStopDaemonFailContinuesForTeardown(t *testing.T) {
	// Daemon StopVM failure is swallowed silently so teardown still
	// proceeds to disk deletion. Common causes: daemon down, or the
	// VM was never supervised by THIS daemon (e.g., adopted on
	// restart and already torn down externally). In every case the
	// user's intent — "stop and destroy" — is achievable via
	// tart.Delete regardless of the daemon's response.
	repoRoot := t.TempDir()
	admin := &fakeStopClient{stopErr: errors.New("daemon down")}
	tr := fakeTartBin(t, repoRoot)
	out := &bytes.Buffer{}

	deps := StopDeps{
		Tart:             tr,
		ServiceAPIClient: admin,
		Out:              out,
	}
	rc, err := RunStop(context.Background(), deps, "proj-123", StopDestroy, true)
	require.NoError(t, err)
	assert.Equal(t, 0, rc)
	assert.Equal(t, 1, admin.stopCalled, "daemon stop must still be attempted")
	assert.Contains(t, out.String(), "Deleted VM proj-123", "disk delete must still run")
}

func TestRunStopDestroy_RemovesStateSnapshot(t *testing.T) {
	// A stale daemon-side snapshot must not survive teardown and leak
	// into a subsequently recreated project. Teardown must wipe it so
	// the next cold-start (or reconcile) starts from a clean baseline.
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, serviceapi.WriteStateSnapshot(identity.Prod, "proj-123", serviceapi.StateSnapshot{
		Cfg: schema.Config{Project: schema.Project{Name: "proj-123"}},
	}))

	repoRoot := t.TempDir()
	admin := &fakeStopClient{}
	tr := fakeTartBin(t, repoRoot)
	out := &bytes.Buffer{}

	deps := StopDeps{
		Tart:             tr,
		ServiceAPIClient: admin,
		In:               strings.NewReader("y\n"),
		Out:              out,
	}
	rc, err := RunStop(context.Background(), deps, "proj-123", StopDestroy, false)
	require.NoError(t, err)
	assert.Equal(t, 0, rc)

	got, err := serviceapi.ReadStateSnapshot(identity.Prod, "proj-123")
	require.NoError(t, err)
	assert.Nil(t, got, "state snapshot must be removed after teardown")
}

func TestRunStopDestroy_RemovesSSHState(t *testing.T) {
	// SSH key material must not survive teardown and leak into a
	// subsequently recreated project. Teardown must wipe the per-project
	// ssh subtree so the next cold-start starts from a clean baseline.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DEVM_RUNTIME_DIR", filepath.Join(t.TempDir(), "rd"))
	require.NoError(t, serviceapi.WriteStateSnapshot(identity.Prod, "proj-123", serviceapi.StateSnapshot{
		Cfg: schema.Config{Project: schema.Project{Name: "proj-123"}},
	}))
	_, err := sshkeys.EnsureProjectKeypair(identity.Prod, "proj-123")
	require.NoError(t, err)

	// Verify SSH directory exists before teardown
	sshDir := sshkeys.ProjectDir(identity.Prod, "proj-123")
	_, err = os.Stat(sshDir)
	require.NoError(t, err, "ssh project dir must exist before teardown")

	repoRoot := t.TempDir()
	admin := &fakeStopClient{}
	tr := fakeTartBin(t, repoRoot)
	out := &bytes.Buffer{}

	deps := StopDeps{
		Tart:             tr,
		ServiceAPIClient: admin,
		In:               strings.NewReader("y\n"),
		Out:              out,
	}
	rc, err := RunStop(context.Background(), deps, "proj-123", StopDestroy, false)
	require.NoError(t, err)
	assert.Equal(t, 0, rc)

	// Verify SSH directory is gone after teardown
	_, err = os.Stat(sshDir)
	assert.True(t, os.IsNotExist(err), "ssh project dir must be gone after --destroy")
}

func TestRunStopPromptText(t *testing.T) {
	// StopPreserve prompt says "Stop VM"
	admin := &fakeStopClient{}
	inStop := strings.NewReader("n\n")
	outStop := &bytes.Buffer{}
	deps := StopDeps{
		Tart:             tartPathNotNeeded(t),
		ServiceAPIClient: admin,
		In:               inStop,
		Out:              outStop,
	}
	_, err := RunStop(context.Background(), deps, "proj-123", StopPreserve, false)
	require.NoError(t, err)
	assert.Contains(t, outStop.String(), "Stop VM proj-123")

	// StopDestroy prompt says "Tear down VM"
	inTear := strings.NewReader("n\n")
	outTear := &bytes.Buffer{}
	deps2 := StopDeps{
		Tart:             tartPathNotNeeded(t),
		ServiceAPIClient: &fakeStopClient{},
		In:               inTear,
		Out:              outTear,
	}
	_, err = RunStop(context.Background(), deps2, "proj-123", StopDestroy, false)
	require.NoError(t, err)
	assert.Contains(t, outTear.String(), "Tear down VM proj-123")
}

func TestDestructivenessIdentity(t *testing.T) {
	assert.NotEqual(t, StopPreserve, StopDestroy)
}
