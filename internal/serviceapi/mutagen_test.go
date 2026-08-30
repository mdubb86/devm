package serviceapi

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/mutagen"
	"github.com/mdubb86/devm/internal/supervisor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testMutagenCfg returns an identity.Config pointed at a scratch HOME so
// RuntimeDir()-derived paths never touch the real ~/Library.
func testMutagenCfg(t *testing.T) identity.Config {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	return identity.Config{Name: "devm-test-mutagen"}
}

// spawnFakeAliveProcess starts a real, short-lived child process and
// returns its PID — supervisor.Status/Stop only check liveness via
// kill(pid, 0) and signal delivery, they don't care what the process is.
// Cleaned up via t.Cleanup regardless of whether the test itself stops it.
func spawnFakeAliveProcess(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	require.NoError(t, cmd.Start())
	pid := cmd.Process.Pid
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			_ = syscall.Kill(pid, syscall.SIGKILL)
			<-done
		}
	})
	return pid
}

func stubMutagenSpawnSeams(t *testing.T) (ensureCalled *bool, startCalled *bool) {
	t.Helper()
	origEnsure := mutagenEnsureFn
	origStart := mutagenDaemonStartFn
	t.Cleanup(func() {
		mutagenEnsureFn = origEnsure
		mutagenDaemonStartFn = origStart
	})
	ensureCalled = new(bool)
	startCalled = new(bool)
	mutagenEnsureFn = func(string) (string, error) {
		*ensureCalled = true
		return "/fake/bin/mutagen", nil
	}
	mutagenDaemonStartFn = func(*mutagen.CLI) (int, error) {
		*startCalled = true
		// supervisor.Status verifies liveness via kill(pid, 0), so the
		// fake spawn must return a PID that's actually alive — the test
		// process's own PID is harmless to probe that way (signal 0
		// never delivers a real signal).
		return os.Getpid(), nil
	}
	return ensureCalled, startCalled
}

func TestSpawnMutagen_CreatesDataDirAndSpawns(t *testing.T) {
	cfg := testMutagenCfg(t)
	sup := supervisor.New(t.TempDir())

	origEnsure := mutagenEnsureFn
	origStart := mutagenDaemonStartFn
	t.Cleanup(func() {
		mutagenEnsureFn = origEnsure
		mutagenDaemonStartFn = origStart
	})

	var ensureCalledWith string
	mutagenEnsureFn = func(runtimeDir string) (string, error) {
		ensureCalledWith = runtimeDir
		return "/fake/bin/mutagen", nil
	}
	var startCalledWith *mutagen.CLI
	mutagenDaemonStartFn = func(cli *mutagen.CLI) (int, error) {
		startCalledWith = cli
		return os.Getpid(), nil
	}

	err := SpawnMutagen(context.Background(), cfg, sup)
	require.NoError(t, err)

	assert.Equal(t, cfg.RuntimeDir(), ensureCalledWith)
	require.NotNil(t, startCalledWith)
	assert.Equal(t, "/fake/bin/mutagen", startCalledWith.Binary)

	info, err := os.Stat(mutagenDataDir(cfg))
	require.NoError(t, err)
	assert.True(t, info.IsDir())

	st := sup.Status(supervisor.Key{Role: supervisor.RoleMutagen})
	assert.True(t, st.Present)
	assert.Equal(t, os.Getpid(), st.PID)
}

func TestSpawnMutagen_SetsMutagenDataDirectoryEnv(t *testing.T) {
	cfg := testMutagenCfg(t)
	sup := supervisor.New(t.TempDir())

	origEnsure := mutagenEnsureFn
	origStart := mutagenDaemonStartFn
	t.Cleanup(func() {
		mutagenEnsureFn = origEnsure
		mutagenDaemonStartFn = origStart
	})

	mutagenEnsureFn = func(string) (string, error) { return "/fake/bin/mutagen", nil }
	var startCalledWith *mutagen.CLI
	mutagenDaemonStartFn = func(cli *mutagen.CLI) (int, error) {
		startCalledWith = cli
		return 1, nil
	}

	require.NoError(t, SpawnMutagen(context.Background(), cfg, sup))

	require.NotNil(t, startCalledWith)
	assert.Equal(t, mutagenDataDir(cfg), startCalledWith.DataDir)
}

func TestSpawnMutagen_SetsMUTAGEN_SSH_PATHEnv(t *testing.T) {
	cfg := testMutagenCfg(t)
	sup := supervisor.New(t.TempDir())

	origEnsure := mutagenEnsureFn
	origStart := mutagenDaemonStartFn
	t.Cleanup(func() {
		mutagenEnsureFn = origEnsure
		mutagenDaemonStartFn = origStart
	})

	mutagenEnsureFn = func(string) (string, error) { return "/fake/bin/mutagen", nil }
	var startCalledWith *mutagen.CLI
	mutagenDaemonStartFn = func(cli *mutagen.CLI) (int, error) {
		startCalledWith = cli
		return 1, nil
	}

	require.NoError(t, SpawnMutagen(context.Background(), cfg, sup))

	require.NotNil(t, startCalledWith)
	assert.Contains(t, startCalledWith.ExtraEnv, "MUTAGEN_SSH_PATH="+MutagenSSHDir(cfg))
}

// AdoptMutagenDaemon always stops and respawns any existing mutagen
// daemon on devm daemon start, regardless of binary sha. Mutagen
// sessions live in DataDir and are resumed automatically on the fresh
// daemon; this guarantees the daemon inherits the current build's env
// (notably HOME → devm's managed ssh_config include, so ssh under root
// sees per-project Host blocks). Adopt-in-place was silently pinning a
// stale env from a previous devm build.
func TestAdoptMutagenDaemon_ExistingAlive_StopsAndRespawns(t *testing.T) {
	cfg := testMutagenCfg(t)
	sup := supervisor.New(t.TempDir())
	pid := spawnFakeAliveProcess(t)

	origLock := mutagenLockPID
	origSha := mutagenBinarySha
	t.Cleanup(func() {
		mutagenLockPID = origLock
		mutagenBinarySha = origSha
	})
	mutagenLockPID = func(string) (int, error) { return pid, nil }
	mutagenBinarySha = func(identity.Config) (string, error) { return mutagen.EmbeddedSha256(), nil }

	ensureCalled, startCalled := stubMutagenSpawnSeams(t)

	err := AdoptMutagenDaemon(context.Background(), cfg, sup)
	require.NoError(t, err)
	assert.True(t, *ensureCalled, "existing daemon must be replaced")
	assert.True(t, *startCalled, "existing daemon must be replaced")
}

func TestAdoptMutagenDaemon_ExistingAliveShaMismatches_StopsAndRespawns(t *testing.T) {
	cfg := testMutagenCfg(t)
	sup := supervisor.New(t.TempDir())
	pid := spawnFakeAliveProcess(t)

	origLock := mutagenLockPID
	origSha := mutagenBinarySha
	t.Cleanup(func() {
		mutagenLockPID = origLock
		mutagenBinarySha = origSha
	})
	mutagenLockPID = func(string) (int, error) { return pid, nil }
	mutagenBinarySha = func(identity.Config) (string, error) { return "stale-sha-does-not-match", nil }

	ensureCalled, startCalled := stubMutagenSpawnSeams(t)

	err := AdoptMutagenDaemon(context.Background(), cfg, sup)
	require.NoError(t, err)
	assert.True(t, *ensureCalled, "sha mismatch must respawn")
	assert.True(t, *startCalled, "sha mismatch must respawn")

	// The stale process must have actually been signaled dead.
	assert.Eventually(t, func() bool {
		return syscall.Kill(pid, 0) != nil
	}, 2*time.Second, 20*time.Millisecond, "stale daemon pid must be stopped")

	st := sup.Status(supervisor.Key{Role: supervisor.RoleMutagen})
	assert.True(t, st.Present)
	assert.Equal(t, os.Getpid(), st.PID, "must be adopted under the freshly-spawned pid")
}

func TestAdoptMutagenDaemon_NoneAlive_Spawns(t *testing.T) {
	cfg := testMutagenCfg(t)
	sup := supervisor.New(t.TempDir())

	origLock := mutagenLockPID
	t.Cleanup(func() { mutagenLockPID = origLock })
	mutagenLockPID = func(string) (int, error) { return 0, nil }

	ensureCalled, startCalled := stubMutagenSpawnSeams(t)

	err := AdoptMutagenDaemon(context.Background(), cfg, sup)
	require.NoError(t, err)
	assert.True(t, *ensureCalled)
	assert.True(t, *startCalled)

	st := sup.Status(supervisor.Key{Role: supervisor.RoleMutagen})
	assert.True(t, st.Present)
	assert.Equal(t, os.Getpid(), st.PID)
}

func TestStopMutagen_CallsSupervisorStop(t *testing.T) {
	sup := supervisor.New(t.TempDir())
	pid := spawnFakeAliveProcess(t)
	sup.Adopt(supervisor.Key{Role: supervisor.RoleMutagen}, pid)

	require.NoError(t, StopMutagen(sup))

	st := sup.Status(supervisor.Key{Role: supervisor.RoleMutagen})
	assert.False(t, st.Present)
}
