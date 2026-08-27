package serviceapi

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestSpawnMutagen_CreatesDataAndHomeDirsAndSpawns(t *testing.T) {
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

	info, err = os.Stat(mutagenHomeDir(cfg))
	require.NoError(t, err)
	assert.True(t, info.IsDir())

	st := sup.Status(supervisor.Key{Role: supervisor.RoleMutagen})
	assert.True(t, st.Present)
	assert.Equal(t, os.Getpid(), st.PID)
}

func TestSpawnMutagen_SetsEnvIncludingMutagenDataDirectoryAndHome(t *testing.T) {
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
	assert.Contains(t, startCalledWith.ExtraEnv, "HOME="+mutagenHomeDir(cfg))
}

func TestAdoptMutagenDaemon_ExistingAliveShaMatches_Adopts(t *testing.T) {
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
	assert.False(t, *ensureCalled, "sha match must adopt in place, not respawn")
	assert.False(t, *startCalled, "sha match must adopt in place, not respawn")

	st := sup.Status(supervisor.Key{Role: supervisor.RoleMutagen})
	assert.True(t, st.Present)
	assert.Equal(t, pid, st.PID)
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

func TestWriteMutagenSSHConfig_WritesConfigDIncludeAndHostBlock(t *testing.T) {
	cfg := testMutagenCfg(t)

	err := WriteMutagenSSHConfig(cfg, "myproj", "devm-myproj", "192.168.64.10", "/path/to/id_ed25519", "/path/to/known_hosts")
	require.NoError(t, err)

	path := filepath.Join(mutagenHomeDir(cfg), ".ssh", "config.d", "myproj.conf")
	body, err := os.ReadFile(path)
	require.NoError(t, err)

	want := `Host devm-myproj
  HostName 192.168.64.10
  User devm
  IdentityFile /path/to/id_ed25519
  UserKnownHostsFile /path/to/known_hosts
  StrictHostKeyChecking yes
  IdentitiesOnly yes
`
	assert.Equal(t, want, string(body))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
}

func TestWriteMutagenSSHConfig_MainConfigContainsIncludeDirective(t *testing.T) {
	cfg := testMutagenCfg(t)

	err := WriteMutagenSSHConfig(cfg, "myproj", "devm-myproj", "192.168.64.10", "/path/to/id_ed25519", "/path/to/known_hosts")
	require.NoError(t, err)

	mainPath := filepath.Join(mutagenHomeDir(cfg), ".ssh", "config")
	body, err := os.ReadFile(mainPath)
	require.NoError(t, err)
	assert.Contains(t, string(body), "Include config.d/*.conf")

	info, err := os.Stat(mainPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())

	// Idempotent: writing a second project must not duplicate the
	// Include directive.
	require.NoError(t, WriteMutagenSSHConfig(cfg, "otherproj", "devm-otherproj", "192.168.64.11", "/k", "/kh"))
	body2, err := os.ReadFile(mainPath)
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(body2), "Include config.d/*.conf"))
}

func TestRemoveMutagenSSHConfig_DeletesPerProjectFile(t *testing.T) {
	cfg := testMutagenCfg(t)
	require.NoError(t, WriteMutagenSSHConfig(cfg, "myproj", "devm-myproj", "192.168.64.10", "/k", "/kh"))

	path := filepath.Join(mutagenHomeDir(cfg), ".ssh", "config.d", "myproj.conf")
	_, err := os.Stat(path)
	require.NoError(t, err)

	require.NoError(t, RemoveMutagenSSHConfig(cfg, "myproj"))

	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err))

	// Removing a project with no config file is a no-op, not an error.
	require.NoError(t, RemoveMutagenSSHConfig(cfg, "myproj"))
}
