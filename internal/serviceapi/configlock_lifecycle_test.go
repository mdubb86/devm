//go:build darwin

package serviceapi

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/supervisor"
)

// fakeTartScript writes an executable named "tart" into a fresh temp
// directory and returns its path. It answers just enough of the tart
// CLI surface for a /vm/start run to reach past the config-lock step:
//   - `list --format json`   -> "[]" (no existing VM, so start clones)
//   - `clone ...`            -> exit 0
//   - `run ...`              -> exit 0 (built by tr.Run, invoked by the
//     supervisor's Spawn on tr.Path; the script never has to behave
//     like a real VM, only to start successfully)
//   - `exec <name> true`     -> exit 0 (waitVMExecReady's guest-ready
//     probe succeeds immediately instead of retrying for up to 60s)
//   - `exec -i <name> ...`   -> exit 1 (the first VM-config inject step
//     fails on purpose, so the handler returns a deterministic,
//     immediate error whose message has nothing to do with config-lock
//     — the fixed point every /vm/start test in this file asserts
//     against)
func fakeTartScript(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "tart")
	script := `#!/bin/sh
case "$1" in
  list) echo '[]' ;;
  clone) exit 0 ;;
  run) exit 0 ;;
  exec)
    if [ "$2" = "-i" ]; then
      echo "fake inject failure" >&2
      exit 1
    fi
    exit 0
    ;;
  *) exit 0 ;;
esac
`
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}

// newTestServerWithVMAndFakeTart wires a /vm/start-capable server whose
// tart binary is fakeTartScript, both as tr.Path (tr.List/tr.Clone/
// tr.Run's spawned process) and on $PATH under the literal name "tart"
// (the hardcoded `exec.Command("tart", ...)` calls in
// waitVMExecReady and the VM-config inject loop resolve the binary via
// $PATH, not via tr.Path).
func newTestServerWithVMAndFakeTart(t *testing.T) (*Server, func()) {
	t.Helper()
	bin := fakeTartScript(t)
	t.Setenv("PATH", filepath.Dir(bin)+":"+os.Getenv("PATH"))
	t.Setenv("DEVM_RUNTIME_DIR", t.TempDir())

	logDir := t.TempDir()
	sup := supervisor.New(logDir)
	tr := tart.New()
	tr.Path = bin

	return newTestServerWithVM(t, sup, tr)
}

// TestVMStart_LocksConfig_DefaultEnabled verifies /vm/start makes
// devm.yaml host-immutable and registers the project in
// configLockState before the VM-config inject step runs — proving the
// lock lands before the guest could ever see a writable window, not
// just "eventually". ConfigLock is left unset on the request, so the
// default-on behavior (Config.ConfigLockEnabled) applies.
func TestVMStart_LocksConfig_DefaultEnabled(t *testing.T) {
	srv, cleanup := newTestServerWithVMAndFakeTart(t)
	defer cleanup()

	repoRoot := t.TempDir()
	cfgPath := filepath.Join(repoRoot, "devm.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("project:\n  name: p\n"), 0o644))
	t.Cleanup(func() { _ = unlockConfigFiles(repoRoot) })

	const name = "start-lock-default"
	t.Cleanup(func() { configLockState.del(name) })

	c := NewClientWithSocket(srv.socketPath)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := c.StartVM(ctx, VMStartRequest{
		Name:              name,
		WorkspaceHostPath: repoRoot,
		Cfg:               schema.Config{Project: schema.Project{Name: name}},
	})
	// The fake tart's inject step deliberately fails so the handler
	// returns fast and deterministically; this is not the assertion
	// under test, just proof the failure is unrelated to config-lock.
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vm inject step")

	assert.True(t, isImmutable(t, cfgPath), "devm.yaml must be locked before the VM inject step runs")
	entry, ok := configLockState.get(name)
	require.True(t, ok, "configLockState must hold an entry after a successful lock")
	assert.Equal(t, repoRoot, entry.repoRoot)
}

// TestVMStart_ConfigLockDisabled_NoLock verifies that `config_lock:
// false` on the request's Cfg opts a project out of the host-immutable
// lock entirely: no chflags, no configLockState entry.
func TestVMStart_ConfigLockDisabled_NoLock(t *testing.T) {
	srv, cleanup := newTestServerWithVMAndFakeTart(t)
	defer cleanup()

	repoRoot := t.TempDir()
	cfgPath := filepath.Join(repoRoot, "devm.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("project:\n  name: p\n"), 0o644))
	t.Cleanup(func() { _ = unlockConfigFiles(repoRoot) })

	const name = "start-lock-disabled"
	t.Cleanup(func() { configLockState.del(name) })

	disabled := false
	c := NewClientWithSocket(srv.socketPath)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := c.StartVM(ctx, VMStartRequest{
		Name:              name,
		WorkspaceHostPath: repoRoot,
		Cfg: schema.Config{
			Project:    schema.Project{Name: name},
			ConfigLock: &disabled,
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vm inject step")

	assert.False(t, isImmutable(t, cfgPath), "config_lock:false must not lock devm.yaml")
	_, ok := configLockState.get(name)
	assert.False(t, ok, "config_lock:false must not register a configLockState entry")
}

// TestVMStart_LockFailureIsBestEffort verifies that a lockConfigFiles
// failure never surfaces as the /vm/start error and never blocks the
// rest of the handler. devm.yaml is a symlink to itself here — stat
// resolves it and fails with ELOOP, not ENOENT, so setImmutable can't
// take its usual "missing file is a no-op" escape hatch and must return
// a real error for lockConfigFiles to propagate.
func TestVMStart_LockFailureIsBestEffort(t *testing.T) {
	srv, cleanup := newTestServerWithVMAndFakeTart(t)
	defer cleanup()

	repoRoot := t.TempDir()
	cfgPath := filepath.Join(repoRoot, "devm.yaml")
	require.NoError(t, os.Symlink("devm.yaml", cfgPath))

	const name = "start-lock-besteffort"
	t.Cleanup(func() { configLockState.del(name) })

	c := NewClientWithSocket(srv.socketPath)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := c.StartVM(ctx, VMStartRequest{
		Name:              name,
		WorkspaceHostPath: repoRoot,
		Cfg:               schema.Config{Project: schema.Project{Name: name}},
	})
	// Must still fail at the (unrelated) inject step, proving the lock
	// failure was swallowed rather than short-circuiting the handler
	// with its own http.Error.
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vm inject step")
	assert.NotContains(t, err.Error(), "lock")

	_, ok := configLockState.get(name)
	assert.False(t, ok, "a failed lock must not register a configLockState entry")
}

// TestVMStop_UnlocksConfig_FromRegistry verifies /vm/stop clears the
// host-immutable flag and drops the configLockState entry using the
// registry populated at /vm/start — the normal (non-restart) path.
func TestVMStop_UnlocksConfig_FromRegistry(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", t.TempDir())
	logDir := t.TempDir()
	sup := supervisor.New(logDir)
	bin := filepath.Join(t.TempDir(), "tart-fake")
	script := "#!/bin/sh\ncase \"$1\" in\n  list) echo '[]' ;;\nesac\nexit 0\n"
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
	tr := tart.New()
	tr.Path = bin

	srv, cleanup := newTestServerWithVM(t, sup, tr)
	defer cleanup()

	repoRoot := t.TempDir()
	cfgPath := filepath.Join(repoRoot, "devm.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("project:\n  name: p\n"), 0o644))
	t.Cleanup(func() { _ = unlockConfigFiles(repoRoot) })

	const name = "stop-lock-registry"
	require.NoError(t, lockConfigFiles(repoRoot))
	configLockState.put(name, repoRoot)
	t.Cleanup(func() { configLockState.del(name) })
	require.True(t, isImmutable(t, cfgPath), "test setup must start with the file locked")

	c := NewClientWithSocket(srv.socketPath)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	require.NoError(t, c.StopVM(ctx, name))

	assert.False(t, isImmutable(t, cfgPath), "/vm/stop must unlock devm.yaml")
	_, ok := configLockState.get(name)
	assert.False(t, ok, "/vm/stop must clear the configLockState entry")
}

// TestVMStop_UnlocksConfig_FromSnapshotFallback verifies the
// post-restart path: configLockState has no entry (as if the daemon
// restarted and this project hasn't been re-adopted), so /vm/stop
// falls back to the persisted StateSnapshot's WorkspaceHostPath to find
// what to unlock.
func TestVMStop_UnlocksConfig_FromSnapshotFallback(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", t.TempDir())
	logDir := t.TempDir()
	sup := supervisor.New(logDir)
	bin := filepath.Join(t.TempDir(), "tart-fake")
	script := "#!/bin/sh\ncase \"$1\" in\n  list) echo '[]' ;;\nesac\nexit 0\n"
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
	tr := tart.New()
	tr.Path = bin

	srv, cleanup := newTestServerWithVM(t, sup, tr)
	defer cleanup()

	repoRoot := t.TempDir()
	cfgPath := filepath.Join(repoRoot, "devm.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("project:\n  name: p\n"), 0o644))
	t.Cleanup(func() { _ = unlockConfigFiles(repoRoot) })

	const name = "stop-lock-snapshot-fallback"
	require.NoError(t, lockConfigFiles(repoRoot))
	// Deliberately no configLockState.put — simulates a daemon restart
	// that hasn't re-adopted this project yet.
	require.NoError(t, WriteStateSnapshot(name, StateSnapshot{
		Cfg:               schema.Config{Project: schema.Project{Name: name}},
		WorkspaceHostPath: repoRoot,
	}))
	require.True(t, isImmutable(t, cfgPath), "test setup must start with the file locked")

	c := NewClientWithSocket(srv.socketPath)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	require.NoError(t, c.StopVM(ctx, name))

	assert.False(t, isImmutable(t, cfgPath), "/vm/stop must unlock devm.yaml via the state-snapshot fallback")
}

// TestRecoverProjectState_RelocksConfig_WhenEnabled verifies the
// daemon-restart adopt path: recoverProjectState re-locks a recovered
// running project's devm.yaml and repopulates configLockState, so
// "running implies locked" holds again after a restart and a later
// /vm/stop can find the repoRoot to unlock.
func TestRecoverProjectState_RelocksConfig_WhenEnabled(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", t.TempDir())
	const projectID = "adopt-relock-enabled"
	t.Cleanup(func() {
		ironProxyState.del(projectID)
		configLockState.del(projectID)
	})

	repoRoot := t.TempDir()
	cfgPath := filepath.Join(repoRoot, "devm.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("project:\n  name: p\n"), 0o644))
	t.Cleanup(func() { _ = unlockConfigFiles(repoRoot) })

	require.NoError(t, WriteStateSnapshot(projectID, StateSnapshot{
		Cfg:               schema.Config{Project: schema.Project{Name: projectID}},
		WorkspaceHostPath: repoRoot,
	}))

	routes := NewRoutes()
	recoverProjectState(context.Background(), tart.New(), routes, projectID)

	assert.True(t, isImmutable(t, cfgPath), "recoverProjectState must re-lock a recovered running project's devm.yaml")
	entry, ok := configLockState.get(projectID)
	require.True(t, ok, "recoverProjectState must repopulate configLockState")
	assert.Equal(t, repoRoot, entry.repoRoot)
}

// TestRecoverProjectState_DoesNotRelock_WhenDisabled verifies a
// recovered project whose config opted out (`config_lock: false`) is
// left unlocked across a daemon restart, mirroring /vm/start's gating.
func TestRecoverProjectState_DoesNotRelock_WhenDisabled(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", t.TempDir())
	const projectID = "adopt-relock-disabled"
	t.Cleanup(func() {
		ironProxyState.del(projectID)
		configLockState.del(projectID)
	})

	repoRoot := t.TempDir()
	cfgPath := filepath.Join(repoRoot, "devm.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("project:\n  name: p\n"), 0o644))
	t.Cleanup(func() { _ = unlockConfigFiles(repoRoot) })

	disabled := false
	require.NoError(t, WriteStateSnapshot(projectID, StateSnapshot{
		Cfg:               schema.Config{Project: schema.Project{Name: projectID}, ConfigLock: &disabled},
		WorkspaceHostPath: repoRoot,
	}))

	routes := NewRoutes()
	recoverProjectState(context.Background(), tart.New(), routes, projectID)

	assert.False(t, isImmutable(t, cfgPath), "config_lock:false must not be re-locked on adopt")
	_, ok := configLockState.get(projectID)
	assert.False(t, ok, "config_lock:false must not register a configLockState entry on adopt")
}

// TestVMUnlockConfig_ClearsImmutableFlag verifies `devm unlock`'s
// daemon endpoint (POST /vm/unlock-config) clears the host-immutable
// flag on a locked project's devm.yaml, reports was_locked=true, and
// cancels any pending relock timer without dropping the registry
// entry (repoRoot is still needed for the next lock/reconcile).
func TestVMUnlockConfig_ClearsImmutableFlag(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", t.TempDir())
	logDir := t.TempDir()
	sup := supervisor.New(logDir)
	bin := filepath.Join(t.TempDir(), "tart-fake")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	tr := tart.New()
	tr.Path = bin

	srv, cleanup := newTestServerWithVM(t, sup, tr)
	defer cleanup()

	repoRoot := t.TempDir()
	cfgPath := filepath.Join(repoRoot, "devm.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("project:\n  name: p\n"), 0o644))
	t.Cleanup(func() { _ = unlockConfigFiles(repoRoot) })

	const name = "unlock-config-clears"
	t.Cleanup(func() { configLockState.del(name) })
	require.NoError(t, lockConfigFiles(repoRoot))
	configLockState.put(name, repoRoot)
	require.True(t, isImmutable(t, cfgPath), "test setup must start with the file locked")

	c := NewClientWithSocket(srv.socketPath)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	wasLocked, err := c.UnlockConfig(ctx, name)
	require.NoError(t, err)
	assert.True(t, wasLocked, "unlock-config must report the project was locked")

	assert.False(t, isImmutable(t, cfgPath), "/vm/unlock-config must clear the immutable flag")
	entry, ok := configLockState.get(name)
	require.True(t, ok, "the registry entry must survive an unlock — repoRoot is still needed")
	assert.Equal(t, repoRoot, entry.repoRoot)
}

// TestVMLockConfig_ReLocksFile verifies `devm lock`'s daemon endpoint
// (POST /vm/lock-config) re-applies the host-immutable flag on a
// project that was previously unlocked.
func TestVMLockConfig_ReLocksFile(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", t.TempDir())
	logDir := t.TempDir()
	sup := supervisor.New(logDir)
	bin := filepath.Join(t.TempDir(), "tart-fake")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	tr := tart.New()
	tr.Path = bin

	srv, cleanup := newTestServerWithVM(t, sup, tr)
	defer cleanup()

	repoRoot := t.TempDir()
	cfgPath := filepath.Join(repoRoot, "devm.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("project:\n  name: p\n"), 0o644))
	t.Cleanup(func() { _ = unlockConfigFiles(repoRoot) })

	const name = "lock-config-relocks"
	t.Cleanup(func() { configLockState.del(name) })
	// Registered but currently unlocked — the state right after a
	// `devm unlock`.
	configLockState.put(name, repoRoot)
	require.False(t, isImmutable(t, cfgPath), "test setup must start with the file unlocked")

	c := NewClientWithSocket(srv.socketPath)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	require.NoError(t, c.LockConfig(ctx, name))

	assert.True(t, isImmutable(t, cfgPath), "/vm/lock-config must re-apply the immutable flag")
}

// TestVMUnlockConfig_UnknownProject_NoOpNoError verifies POST
// /vm/unlock-config for a project with no configLockState entry (VM
// not running, or config_lock:false) returns 200 was_locked=false
// rather than an error.
func TestVMUnlockConfig_UnknownProject_NoOpNoError(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", t.TempDir())
	logDir := t.TempDir()
	sup := supervisor.New(logDir)
	bin := filepath.Join(t.TempDir(), "tart-fake")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	tr := tart.New()
	tr.Path = bin

	srv, cleanup := newTestServerWithVM(t, sup, tr)
	defer cleanup()

	c := NewClientWithSocket(srv.socketPath)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	wasLocked, err := c.UnlockConfig(ctx, "no-such-project")
	require.NoError(t, err)
	assert.False(t, wasLocked)
}
