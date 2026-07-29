package main

import (
	"bufio"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// TestShim_ChildRunsInNewSession proves the shim's core promise:
// the target process is in a different session (SID) than the shim
// itself. Without Setsid, both would share the caller's session
// and launchctl bootout of a parent daemon would kill both.
//
// Session id is checked via unix.Getsid rather than `ps`: on this
// platform's BSD ps, the SESS column (the closest analog to GNU ps's
// `sid` keyword, which BSD ps doesn't have at all) reports a stale/
// unpopulated 0 for every process, making it useless as a signal here.
func TestShim_ChildRunsInNewSession(t *testing.T) {
	shim := buildShim(t)

	// Target: a shell that prints its own PID ($$) then idles, so the
	// test can read the PID and query its session id directly.
	cmd := exec.Command(shim, "sh", "-c", "echo $$; sleep 5")
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	line, err := bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err, "reading child PID from shim's stdout")
	childPID, err := strconv.Atoi(strings.TrimSpace(line))
	require.NoError(t, err, "parsing child PID %q", line)

	childSID, err := unix.Getsid(childPID)
	require.NoError(t, err)

	ownSID, err := unix.Getsid(0)
	require.NoError(t, err)

	assert.NotEqual(t, ownSID, childSID,
		"child SID (%d) must differ from parent SID (%d) — Setsid didn't detach",
		childSID, ownSID)
}

// TestShim_PropagatesExitCode: iron-proxy exit codes must reach pexec
// so OnUnexpectedExit / backoff can react correctly.
func TestShim_PropagatesExitCode(t *testing.T) {
	shim := buildShim(t)

	cmd := exec.Command(shim, "sh", "-c", "exit 42")
	err := cmd.Run()
	require.Error(t, err)
	exitErr, ok := err.(*exec.ExitError)
	require.True(t, ok, "expected *exec.ExitError, got %T: %v", err, err)
	assert.Equal(t, 42, exitErr.ExitCode())
}

// TestShim_UsageErrorWhenNoArgs — running the shim with no args
// must exit non-zero with a usable message (not crash).
func TestShim_UsageErrorWhenNoArgs(t *testing.T) {
	shim := buildShim(t)

	err := exec.Command(shim).Run()
	require.Error(t, err)
	exitErr, ok := err.(*exec.ExitError)
	require.True(t, ok)
	assert.NotEqual(t, 0, exitErr.ExitCode())
}

// TestShim_ChildSurvivesShimDeath proves the shim's core job: when
// the shim itself dies (SIGKILL — mirrors launchctl bootout killing
// the daemon), the child it spawned continues running because it's
// in a new session, detached from the shim's process tree.
//
// Without the shim's Setsid: killing the shim would take the child
// with it (same session cleanup).
func TestShim_ChildSurvivesShimDeath(t *testing.T) {
	shim := buildShim(t)

	// Launch shim with a sleep long enough to observe post-shim-death survival.
	cmd := exec.Command(shim, "sleep", "30")
	require.NoError(t, cmd.Start())

	// Find the sleep child (grandchild from shim's exec.Cmd perspective).
	// The shim is `cmd.Process.Pid`; the sleep child is a child OF the shim.
	// Poll rather than a fixed sleep: fork+exec+setsid timing varies under load.
	var childPID int
	deadlineStart := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadlineStart) {
		childPID = findSleepChildOf(t, cmd.Process.Pid)
		if childPID != 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.NotZero(t, childPID, "expected to find sleep child of shim %d", cmd.Process.Pid)

	// Sanity: child is alive.
	require.NoError(t, syscall.Kill(childPID, 0), "child should be alive before shim killed")

	// Verify child is in a different session — sanity that setsid ran.
	shimSID, _ := unix.Getsid(cmd.Process.Pid)
	childSID, _ := unix.Getsid(childPID)
	require.NotEqual(t, shimSID, childSID,
		"shim SID=%d child SID=%d — setsid didn't detach the child", shimSID, childSID)

	// SIGKILL the shim — mirrors what launchctl bootout does to the devm daemon.
	require.NoError(t, cmd.Process.Kill())
	_ = cmd.Wait()

	// Poll: child should still be alive. If we don't detach via setsid,
	// the child would die with the shim. With setsid, child survives.
	// Give it a couple of seconds; polling accounts for scheduling jitter.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(childPID, 0); err != nil {
			t.Fatalf("child (pid %d) died with shim: %v — setsid detach failed", childPID, err)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Cleanup: kill the child (it's a real sleep process now orphaned to launchd).
	_ = syscall.Kill(childPID, syscall.SIGTERM)
}

// findSleepChildOf returns the PID of the sleep process that's a direct
// child of parentPID. Uses `pgrep -P <parent> sleep`. Returns 0 if none.
func findSleepChildOf(t *testing.T, parentPID int) int {
	t.Helper()
	out, err := exec.Command("pgrep", "-P", strconv.Itoa(parentPID), "sleep").CombinedOutput()
	if err != nil {
		// pgrep returns exit 1 when no match; treat as zero rather than error
		return 0
	}
	lines := strings.Fields(strings.TrimSpace(string(out)))
	if len(lines) == 0 {
		return 0
	}
	pid, err := strconv.Atoi(lines[0])
	require.NoError(t, err)
	return pid
}

// buildShim compiles the shim into a temp dir and returns its path.
func buildShim(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "shim")
	build := exec.Command("go", "build", "-o", out, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build shim: %v\n%s", err, output)
	}
	return out
}
