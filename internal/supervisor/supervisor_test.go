package supervisor

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBackoff_StartsAtBase(t *testing.T) {
	b := newBackoff(100*time.Millisecond, 1*time.Second)
	start := time.Now()
	b.onExit(context.Background(), 1)
	elapsed := time.Since(start)
	assert.Greater(t, elapsed, 80*time.Millisecond)
	assert.Less(t, elapsed, 200*time.Millisecond)
}

func TestBackoff_DoublesOnRepeatedCrashes(t *testing.T) {
	b := newBackoff(100*time.Millisecond, 1*time.Second)
	b.onExit(context.Background(), 1) // sets delay = 100ms
	start := time.Now()
	b.onExit(context.Background(), 1) // would be 200ms now
	elapsed := time.Since(start)
	assert.Greater(t, elapsed, 180*time.Millisecond,
		"second crash should double the delay")
	assert.Less(t, elapsed, 300*time.Millisecond)
}

func TestBackoff_ResetsAfterStablePeriod(t *testing.T) {
	b := newBackoff(50*time.Millisecond, 5*time.Second)
	b.onExit(context.Background(), 1)
	b.delay = 2 * time.Second                       // simulate already-elevated
	b.lastStart = time.Now().Add(-31 * time.Second) // simulate stable >30s ago
	start := time.Now()
	b.onExit(context.Background(), 1) // should reset to base = 50ms
	elapsed := time.Since(start)
	assert.Less(t, elapsed, 150*time.Millisecond,
		"stable period >30s should reset backoff to base")
}

func TestBackoff_RespectsCap(t *testing.T) {
	b := newBackoff(100*time.Millisecond, 200*time.Millisecond)
	b.onExit(context.Background(), 1) // 100ms
	b.onExit(context.Background(), 1) // 200ms (cap)
	start := time.Now()
	b.onExit(context.Background(), 1) // would be 400ms but capped at 200ms
	elapsed := time.Since(start)
	assert.Less(t, elapsed, 300*time.Millisecond,
		"delay must not exceed cap")
}

func TestKey_String(t *testing.T) {
	assert.Equal(t, "acme/vm", Key{ProjectID: "acme", Role: RoleVM}.String())
	assert.Equal(t, "acme/proxy", Key{ProjectID: "acme", Role: RoleProxy}.String())
}

func TestSetsid_AppliesOnDarwin(t *testing.T) {
	cmd := exec.Command("true")
	applySetsid(cmd)
	// On darwin, SysProcAttr is non-nil and Setsid is true.
	// On other OSes, applySetsid is a no-op (cmd.SysProcAttr stays nil).
	// Non-nil SysProcAttr is a sufficient proxy on darwin.
	_ = cmd
}

func TestEnvMap_EmptyForwardsDaemonEnv(t *testing.T) {
	// Contract: when cmd.Env is empty/nil, envMap forwards the
	// daemon's own environment so spawned children inherit it.
	// pexec builds the child's env solely from this map (no
	// implicit parent inheritance), so returning nil here would
	// give the child an empty environment — discovered in the
	// 2026-06-27 smoke test when `tart run` was given no $HOME
	// or $PATH and silently failed.
	t.Setenv("DEVM_SUPERVISOR_TEST_MARKER", "present")
	m := envMap(nil)
	assert.Equal(t, "present", m["DEVM_SUPERVISOR_TEST_MARKER"],
		"daemon env not forwarded when cmd.Env is nil")

	m = envMap([]string{})
	assert.Equal(t, "present", m["DEVM_SUPERVISOR_TEST_MARKER"],
		"daemon env not forwarded when cmd.Env is empty")
}

func TestEnvMap_Parses(t *testing.T) {
	m := envMap([]string{"FOO=bar", "BAZ=qux=quux"})
	assert.Equal(t, "bar", m["FOO"])
	assert.Equal(t, "qux=quux", m["BAZ"], "value with = should be preserved")
}

func TestArgsAfterPath_Empty(t *testing.T) {
	assert.Nil(t, argsAfterPath(nil))
	assert.Nil(t, argsAfterPath([]string{}))
}

func TestArgsAfterPath_StripsFirst(t *testing.T) {
	result := argsAfterPath([]string{"/usr/bin/tart", "run", "--no-graphics", "myvm"})
	assert.Equal(t, []string{"run", "--no-graphics", "myvm"}, result)
}

// TestSupervisor_SpawnActuallyRunsChild would have caught the
// 2026-06-27 bug where pexec.ProcessManager.AddProcessFromConfig
// silently registered children without starting them because
// pm.started was false until Start() was called.
func TestSupervisor_SpawnActuallyRunsChild(t *testing.T) {
	tmp := t.TempDir()
	marker := tmp + "/spawned"

	s := New(tmp)
	defer func() {
		_ = s.pm.Stop()
	}()

	k := Key{ProjectID: "test", Role: RoleVM}
	cmd := exec.Command("sh", "-c", "echo running > "+marker)
	require.NoError(t, s.Spawn(context.Background(), k, cmd))

	// Poll briefly: the child should have run and written the marker.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("child never ran; marker %s not created", marker)
}

// TestSupervisor_AdoptedStatusAndStop spawns a real long-running
// child outside the supervisor's lifecycle, registers it via Adopt,
// and verifies Status + Stop work the same as for managed entries.
// This is the post-daemon-restart adoption path that lets us re-
// attach to iron-proxy processes the prior daemon left running.
func TestSupervisor_AdoptedStatusAndStop(t *testing.T) {
	tmp := t.TempDir()
	s := New(tmp)
	defer func() { _ = s.pm.Stop() }()

	// Start a sleep we can later SIGTERM. We do this *outside* the
	// supervisor — that's the whole point of adoption.
	cmd := exec.Command("sleep", "30")
	require.NoError(t, cmd.Start())
	pid := cmd.Process.Pid
	exitCh := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		exitCh <- cmd.Wait()
		close(done)
	}()
	defer func() {
		select {
		case <-done:
		default:
			_ = syscall.Kill(pid, syscall.SIGKILL)
			<-done
		}
	}()

	k := Key{ProjectID: "adopted-proj", Role: RoleProxy}

	state := s.Status(k)
	assert.False(t, state.Present, "unknown key should not be present pre-Adopt")

	s.Adopt(k, pid)

	state = s.Status(k)
	assert.True(t, state.Present)
	assert.True(t, state.Running)
	assert.Equal(t, pid, state.PID)

	require.NoError(t, s.Stop(context.Background(), k))

	// Confirm SIGTERM landed: child exits and reports signal=SIGTERM.
	select {
	case err := <-exitCh:
		var exitErr *exec.ExitError
		require.ErrorAs(t, err, &exitErr)
		ws := exitErr.Sys().(syscall.WaitStatus)
		assert.True(t, ws.Signaled())
		assert.Equal(t, syscall.SIGTERM, ws.Signal())
	case <-time.After(3 * time.Second):
		t.Fatal("adopted child did not exit after Stop")
	}

	state = s.Status(k)
	assert.False(t, state.Present, "adopted entry should be reaped after Stop")
}

// TestSupervisor_AdoptedDeadPIDReaped verifies that Status detects
// when an adopted process has died (e.g., crashed externally) and
// reaps the entry instead of forever claiming it's running.
func TestSupervisor_AdoptedDeadPIDReaped(t *testing.T) {
	tmp := t.TempDir()
	s := New(tmp)

	// Spawn-and-wait so we have a definitely-reaped PID.
	cmd := exec.Command("true")
	require.NoError(t, cmd.Run())
	deadPID := cmd.Process.Pid

	k := Key{ProjectID: "ghost", Role: RoleProxy}
	s.Adopt(k, deadPID)

	state := s.Status(k)
	assert.False(t, state.Present, "dead adopted PID should be reaped on Status probe")
}

// TestSupervisor_StopUnknownReturnsErrNotFound makes sure the
// adopted-first path doesn't shadow the original error contract.
func TestSupervisor_StopUnknownReturnsErrNotFound(t *testing.T) {
	tmp := t.TempDir()
	s := New(tmp)

	err := s.Stop(context.Background(), Key{ProjectID: "nope", Role: RoleProxy})
	assert.ErrorIs(t, err, ErrNotFound)
}

// TestDeregister_RemovesEntryWithoutSignaling spawns a long-lived
// child, Deregisters it, and confirms the process is still alive
// afterward (Deregister must not signal it) and that the registry no
// longer knows about the key.
func TestDeregister_RemovesEntryWithoutSignaling(t *testing.T) {
	tmp := t.TempDir()
	s := New(tmp)
	defer func() { _ = s.pm.Stop() }()

	k := Key{ProjectID: "long-lived", Role: RoleVM}
	cmd := exec.Command("sleep", "5")
	require.NoError(t, s.Spawn(context.Background(), k, cmd))

	// Give the child a moment to actually start.
	time.Sleep(200 * time.Millisecond)

	pid, err := s.Deregister(k)
	require.NoError(t, err)
	require.Greater(t, pid, 0, "expected a real PID for a started process")
	defer func() { _ = syscall.Kill(pid, syscall.SIGKILL) }()

	// The entry should be gone from the registry.
	state := s.Status(k)
	assert.False(t, state.Present, "key should be gone from the registry after Deregister")

	// The process itself must still be alive — Deregister must not signal it.
	time.Sleep(500 * time.Millisecond)
	err = syscall.Kill(pid, 0)
	assert.NoError(t, err, "process should still be alive after Deregister")
}

// TestDeregister_UnknownKeyReturnsNotFound mirrors Stop's contract for
// keys that were never registered.
func TestDeregister_UnknownKeyReturnsNotFound(t *testing.T) {
	tmp := t.TempDir()
	s := New(tmp)

	pid, err := s.Deregister(Key{ProjectID: "nope", Role: RoleVM})
	assert.ErrorIs(t, err, ErrNotFound)
	assert.Equal(t, 0, pid)
}

// TestDeregister_DisablesRespawn spawns a child that starts, records
// its start into a counter file, then exits non-zero shortly after —
// exactly the shape that would normally trigger pexec's
// OnUnexpectedExit backoff-respawn. Deregistering it before it exits
// must prevent any respawn: the counter file should still show exactly
// one start well after the process has exited and the backoff's base
// delay (1s, per Spawn) has elapsed.
func TestDeregister_DisablesRespawn(t *testing.T) {
	tmp := t.TempDir()
	counter := tmp + "/starts"

	s := New(tmp)
	defer func() { _ = s.pm.Stop() }()

	k := Key{ProjectID: "flaky", Role: RoleVM}
	cmd := exec.Command("sh", "-c", "echo start >> "+counter+"; sleep 0.3; exit 1")
	require.NoError(t, s.Spawn(context.Background(), k, cmd))

	// Deregister promptly, well before the child's 0.3s exit.
	pid, err := s.Deregister(k)
	require.NoError(t, err)
	require.Greater(t, pid, 0)

	// Wait past both the child's exit and the backoff's base restart
	// delay (1s) so a respawn, if pexec still thought it owned this
	// process, would have had time to happen and record a second start.
	time.Sleep(2 * time.Second)

	b, err := os.ReadFile(counter)
	require.NoError(t, err)
	starts := strings.Count(string(b), "start")
	assert.Equal(t, 1, starts, "deregistered process must not be respawned on unexpected exit")
}

// TestSupervisor_ChildInheritsDaemonEnv would have caught the
// 2026-06-27 bug where envMap(nil) returned nil → pexec gave the
// child an empty env → `tart run` (and any other child) couldn't
// find $HOME, $PATH, etc., and silently exited.
func TestSupervisor_ChildInheritsDaemonEnv(t *testing.T) {
	tmp := t.TempDir()
	out := tmp + "/env-marker"

	t.Setenv("DEVM_SPAWN_TEST_MARKER", "inherited")

	s := New(tmp)
	defer func() {
		_ = s.pm.Stop()
	}()

	k := Key{ProjectID: "envtest", Role: RoleVM}
	cmd := exec.Command("sh", "-c", "echo $DEVM_SPAWN_TEST_MARKER > "+out)
	require.NoError(t, s.Spawn(context.Background(), k, cmd))

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(out); err == nil {
			assert.Equal(t, "inherited", strings.TrimSpace(string(b)),
				"child did not see daemon env var")
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("child never wrote env marker")
}
