package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/sandbox/tart"
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
		Ident:            identity.Prod,
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
		Ident:            identity.Prod,
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
		Ident:            identity.Prod,
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
		Ident:            identity.Prod,
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
		Ident:            identity.Prod,
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
		Ident:            identity.Prod,
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
		Ident:            identity.Prod,
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
		Ident:            identity.Prod,
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
		Ident:            identity.Prod,
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

// ---------- mutagenTeardownFn wiring tests (Task 18) ----------

// TestRunStopDestroy_CallsMutagenTeardownBeforeDelete verifies Task 18:
// `devm teardown` terminates the project's mutagen sessions before the
// VM disk is deleted — ordering-consistent with the flush+pause
// /vm/stop issues before the VM itself shuts down.
func TestRunStopDestroy_CallsMutagenTeardownBeforeDelete(t *testing.T) {
	orig := mutagenTeardownFn
	defer func() { mutagenTeardownFn = orig }()

	repoRoot := t.TempDir()
	logPath := filepath.Join(repoRoot, "order.log")
	tr := fakeTartBinWithLog(t, repoRoot, logPath)

	var teardownArgs []string
	mutagenTeardownFn = func(d StopDeps, name string) error {
		fh, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		require.NoError(t, err)
		defer fh.Close()
		_, err = fmt.Fprintln(fh, "MUTAGEN-TEARDOWN "+name)
		require.NoError(t, err)
		teardownArgs = append(teardownArgs, name)
		return nil
	}

	admin := &fakeStopClient{}
	out := &bytes.Buffer{}
	deps := StopDeps{
		Ident:            identity.Prod,
		Tart:             tr,
		ServiceAPIClient: admin,
		Out:              out,
	}
	rc, err := RunStop(context.Background(), deps, "proj-mutagen", StopDestroy, true)
	require.NoError(t, err)
	assert.Equal(t, 0, rc)
	assert.Equal(t, []string{"proj-mutagen"}, teardownArgs, "TeardownPhase must be called for the right projectID")

	logBytes, err := os.ReadFile(logPath)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(logBytes)), "\n")
	teardownIdx, deleteIdx := -1, -1
	for i, line := range lines {
		switch {
		case strings.Contains(line, "MUTAGEN-TEARDOWN"):
			teardownIdx = i
		case strings.Contains(line, "delete"):
			deleteIdx = i
		}
	}
	require.GreaterOrEqual(t, teardownIdx, 0, "mutagen teardown marker must be present")
	require.GreaterOrEqual(t, deleteIdx, 0, "tart delete call must be present")
	assert.Less(t, teardownIdx, deleteIdx, "mutagen sessions must be terminated BEFORE the VM disk is deleted")
}

// TestRunStopPreserve_DoesNotCallMutagenTeardown verifies `devm stop`
// (StopPreserve) never terminates mutagen sessions — only teardown
// permanently ends sync; a plain stop only flushes+pauses (via /vm/stop
// daemon-side, exercised by TestStopPhase_* in internal/serviceapi).
func TestRunStopPreserve_DoesNotCallMutagenTeardown(t *testing.T) {
	orig := mutagenTeardownFn
	defer func() { mutagenTeardownFn = orig }()

	called := false
	mutagenTeardownFn = func(d StopDeps, name string) error {
		called = true
		return nil
	}

	admin := &fakeStopClient{}
	out := &bytes.Buffer{}
	deps := StopDeps{
		Ident:            identity.Prod,
		Tart:             tartPathNotNeeded(t),
		ServiceAPIClient: admin,
		Out:              out,
	}
	rc, err := RunStop(context.Background(), deps, "proj-mutagen", StopPreserve, true)
	require.NoError(t, err)
	assert.Equal(t, 0, rc)
	assert.False(t, called, "StopPreserve must not terminate mutagen sessions")
}

// fakeTartBinDeleteAbsent's `delete` prints tart's stable
// "does not exist" stderr and exits 1. RunStop treats this as
// "already absent" and continues; the cleanup pass must still fire
// so v0.10.0-format leftovers get reaped.
func fakeTartBinDeleteAbsent(t *testing.T, dir string) *tart.Tart {
	t.Helper()
	bin := filepath.Join(dir, "tart-fake-absent")
	script := `#!/bin/sh
if [ "$1" = "delete" ]; then
  echo 'the specified VM "'"$2"'" does not exist' >&2
  exit 1
fi
exec true
`
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
	tr := tart.New()
	tr.Path = bin
	return tr
}

// seedPerProjectArtifacts creates the three orchestration files that
// reapPerProjectArtifacts is expected to remove. Returns the paths so
// tests can assert on each one individually.
func seedPerProjectArtifacts(t *testing.T, cfg identity.Config, name string) (tartDir, ironCfg, softSock string) {
	t.Helper()
	paths := perProjectArtifactPaths(cfg, name)
	require.Len(t, paths, 3, "expected iron-proxy config, softnet sock, tart vm dir")
	ironCfg = paths[0]
	softSock = paths[1]
	tartDir = paths[2]

	// Iron-proxy config: a plain file that would linger after teardown.
	require.NoError(t, os.MkdirAll(filepath.Dir(ironCfg), 0o700))
	require.NoError(t, os.WriteFile(ironCfg, []byte("dns:\n  enabled: true\n"), 0o600))

	// Softnet control sock: a plain file at the deterministic sock path.
	require.NoError(t, os.MkdirAll(filepath.Dir(softSock), 0o700))
	require.NoError(t, os.WriteFile(softSock, []byte(""), 0o600))

	// Tart VM dir: mimics the v0.10.0-format residue tart can't parse.
	require.NoError(t, os.MkdirAll(tartDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tartDir, "config.json"), []byte(`{"schema":"v0.10"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(tartDir, "disk.img"), []byte("fake disk"), 0o644))
	return
}

// TestRunStopDestroy_ReapsStraysOnDeletePath pins that a happy-path
// teardown (tart delete succeeds) still runs the cleanup pass. If
// tart cleaned its own dir up we shouldn't disturb anything else, but
// iron-proxy config and softnet sock always outlive tart and must
// go regardless.
func TestRunStopDestroy_ReapsStraysOnDeletePath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repoRoot := t.TempDir()
	ironCfg, softSock, tartDir := "", "", ""
	tartDir, ironCfg, softSock = seedPerProjectArtifacts(t, identity.Prod, "proj-reap")

	tr := fakeTartBin(t, repoRoot)
	out := &bytes.Buffer{}
	deps := StopDeps{
		Ident:            identity.Prod,
		Tart:             tr,
		ServiceAPIClient: &fakeStopClient{},
		In:               strings.NewReader("y\n"),
		Out:              out,
	}
	rc, err := RunStop(context.Background(), deps, "proj-reap", StopDestroy, false)
	require.NoError(t, err)
	assert.Equal(t, 0, rc)

	for _, p := range []string{ironCfg, softSock, tartDir} {
		_, err := os.Stat(p)
		assert.True(t, os.IsNotExist(err), "expected %s to be gone: %v", p, err)
	}
}

// TestRunStopDestroy_ReapsStraysOnAbsentPath is the jirav regression:
// tart delete says "does not exist" while ~/.tart/vms/<name>/ still
// holds the disk (v0.10.0-format config that newer tart won't parse).
// Teardown must reap the residue on both the "Deleted" and "already
// absent" branches.
func TestRunStopDestroy_ReapsStraysOnAbsentPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repoRoot := t.TempDir()
	tartDir, ironCfg, softSock := seedPerProjectArtifacts(t, identity.Prod, "proj-orphan")

	tr := fakeTartBinDeleteAbsent(t, repoRoot)
	out := &bytes.Buffer{}
	deps := StopDeps{
		Ident:            identity.Prod,
		Tart:             tr,
		ServiceAPIClient: &fakeStopClient{},
		In:               strings.NewReader("y\n"),
		Out:              out,
	}
	rc, err := RunStop(context.Background(), deps, "proj-orphan", StopDestroy, false)
	require.NoError(t, err)
	assert.Equal(t, 0, rc)
	assert.Contains(t, out.String(), "already absent",
		"tart delete's 'does not exist' must be reported as already-absent, not fail teardown")

	for _, p := range []string{ironCfg, softSock, tartDir} {
		_, err := os.Stat(p)
		assert.True(t, os.IsNotExist(err),
			"jirav-class residue at %s must be reaped even when tart said 'already absent': %v", p, err)
	}
}

// TestRunStopDestroy_ReapsWhenArtifactsAbsent pins that a missing
// artifact is not an error — no-op for the common case where tart
// already cleaned itself up cleanly and no iron-proxy ever ran.
func TestRunStopDestroy_ReapsWhenArtifactsAbsent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repoRoot := t.TempDir()
	// Deliberately DON'T seed artifacts.

	tr := fakeTartBin(t, repoRoot)
	out := &bytes.Buffer{}
	deps := StopDeps{
		Ident:            identity.Prod,
		Tart:             tr,
		ServiceAPIClient: &fakeStopClient{},
		In:               strings.NewReader("y\n"),
		Out:              out,
	}
	rc, err := RunStop(context.Background(), deps, "proj-clean", StopDestroy, false)
	require.NoError(t, err)
	assert.Equal(t, 0, rc)
	assert.NotContains(t, out.String(), "warning:",
		"missing artifacts must not produce warning noise")
}

// TestRunStopDestroy_PreservesUserData pins the boundary: teardown
// cleanup MUST NOT touch volumes, secrets, or workspace files.
// Volume preservation across teardown is a documented feature.
func TestRunStopDestroy_PreservesUserData(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repoRoot := t.TempDir()

	// User-data paths that must survive teardown.
	volDir := filepath.Join(identity.Prod.RuntimeDir(), "volumes", "proj-preserve", "claude")
	secretDir := filepath.Join(identity.Prod.SecretsDir(), "proj-preserve")
	require.NoError(t, os.MkdirAll(volDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(volDir, "keep-me.json"), []byte("{}"), 0o600))
	require.NoError(t, os.MkdirAll(secretDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(secretDir, "api_key"), []byte("s3cret"), 0o600))

	tr := fakeTartBin(t, repoRoot)
	out := &bytes.Buffer{}
	deps := StopDeps{
		Ident:            identity.Prod,
		Tart:             tr,
		ServiceAPIClient: &fakeStopClient{},
		In:               strings.NewReader("y\n"),
		Out:              out,
	}
	rc, err := RunStop(context.Background(), deps, "proj-preserve", StopDestroy, false)
	require.NoError(t, err)
	assert.Equal(t, 0, rc)

	_, err = os.Stat(filepath.Join(volDir, "keep-me.json"))
	assert.NoError(t, err, "volume data must survive teardown (documented feature)")
	_, err = os.Stat(filepath.Join(secretDir, "api_key"))
	assert.NoError(t, err, "secrets must survive teardown (documented feature)")
}
