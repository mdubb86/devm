package main

import (
	"bufio"
	"os"
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
	// Kill the shim on any early-exit path so a poll timeout doesn't
	// leave a shim that eventually spawns a `sleep 30` orphan that
	// lingers 30s and contributes to load in subsequent tests.
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	// Find the sleep child (grandchild from shim's exec.Cmd perspective).
	// The shim is `cmd.Process.Pid`; the sleep child is a child OF the shim.
	// Under `go test ./...` peak fork storm, macOS deprioritises freshly-
	// forked processes and the shim can take several seconds just to run
	// Go's runtime init before it ever reaches its own fork/exec of the
	// sleep child. A 10s budget stays tight enough to catch a genuinely
	// hung shim (it would never spawn) while surviving load-driven
	// scheduler starvation.
	var childPID int
	deadlineStart := time.Now().Add(10 * time.Second)
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

// TestShim_IgnoresSIGTERM: the shim's contract post-fix — it MUST
// ignore SIGTERM (and HUP/INT/QUIT) and MUST NOT forward it to the
// child. When launchctl bootout tears down the daemon, launchd
// walks the daemon's session and delivers SIGTERM to every
// descendant sharing that session, including this shim. If the
// shim exits (Go's default) or forwards (the pre-fix behavior),
// iron-proxy dies on every devm install/upgrade — the exact bug
// buzztrack/everstone observed. Post-fix: shim keeps running,
// child keeps running.
func TestShim_IgnoresSIGTERM(t *testing.T) {
	shim := buildShim(t)

	// Same probe shape as TestShim_ChildSurvivesShimDeath.
	cmd := exec.Command(shim, "sleep", "30")
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	// Under `go test ./...` load, the shim can take multi-seconds to
	// finish Go runtime init before spawning its own child; see
	// TestShim_ChildSurvivesShimDeath for the same budget rationale.
	var childPID int
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		childPID = findSleepChildOf(t, cmd.Process.Pid)
		if childPID != 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.NotZero(t, childPID, "expected sleep child of shim %d", cmd.Process.Pid)
	t.Cleanup(func() { _ = syscall.Kill(childPID, syscall.SIGTERM) })

	// SIGTERM the shim — mirrors launchd's bootout signaling the shim
	// as a descendant of the daemon's session.
	require.NoError(t, syscall.Kill(cmd.Process.Pid, syscall.SIGTERM))

	// Poll for a second: BOTH shim and child must remain alive.
	// A pre-fix shim would exit here (Go default = die on SIGTERM),
	// and its old forwarding goroutine would have also SIGTERMed the
	// child (which sleep respects → dies).
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(cmd.Process.Pid, 0); err != nil {
			t.Fatalf("shim (pid %d) exited on SIGTERM — should have ignored it: %v",
				cmd.Process.Pid, err)
		}
		if err := syscall.Kill(childPID, 0); err != nil {
			t.Fatalf("child (pid %d) died — shim must NOT forward SIGTERM: %v",
				childPID, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestShim_ChildSurvivesParentStdoutClose pins the invariant that
// dropped in production and killed iron-proxy for everstone: when the
// process on the OTHER side of the shim's inherited stdout pipe goes
// away, iron-proxy's next write hits EPIPE. Go's runtime handler for
// SIGPIPE on fd 1/2 terminates the process. That's how a daemon
// restart (SIGTERM, SIGKILL, launchctl bootout — any exit) reaches
// through the setsid boundary and kills iron-proxy despite the shim.
//
// This test simulates the daemon's pexec pipe: shim's stdout/stderr is
// a fresh pipe the test owns. The child writes to stdout continuously
// (like iron-proxy's request-audit log). The test closes the pipe's
// read-end — the equivalent of the daemon dying. The child MUST stay
// alive: the shim's job is to absorb the broken-pipe write and keep
// the child unaware.
//
// Fails today because the shim currently passes its inherited stdout
// through verbatim; the child inherits the pipe's write-end and dies
// on the next write. Passes after the shim interposes a fresh pipe
// and a tee-absorb goroutine.
func TestShim_ChildSurvivesParentStdoutClose(t *testing.T) {
	shim := buildShim(t)

	r, w, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })

	// Child writes "tick\n" continuously via `yes tick`. That's what
	// makes the SIGPIPE cascade observable: as soon as the read-end
	// closes, the very next write triggers it.
	cmd := exec.Command(shim, "yes", "tick")
	cmd.Stdout = w
	cmd.Stderr = w
	require.NoError(t, cmd.Start())
	// Close our copy of the write-end. Child + shim still hold theirs.
	_ = w.Close()
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	// Locate the `yes` child of the shim.
	var childPID int
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		childPID = findNamedChildOf(t, cmd.Process.Pid, "yes")
		if childPID != 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.NotZero(t, childPID, "expected `yes` child of shim %d", cmd.Process.Pid)

	// Drain a little output to confirm the pipe is flowing end-to-end
	// before we close it — otherwise a slow start could race the close.
	if err := r.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	buf := make([]byte, 4096)
	n, err := r.Read(buf)
	require.NoError(t, err, "reading initial output from child")
	require.NotZero(t, n, "expected child output before closing pipe")

	// Close the pipe's read-end — this is the moment the daemon "dies".
	// Any subsequent write from the child to stdout will EPIPE.
	require.NoError(t, r.Close())

	// Poll: the child must stay alive. Pre-fix, `yes` sees SIGPIPE on
	// its next write and terminates within milliseconds.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(childPID, 0); err != nil {
			t.Fatalf("child (pid %d) died after parent-pipe close: %v — "+
				"shim did not absorb the broken-pipe write. iron-proxy "+
				"dies the same way on real daemon shutdown.", childPID, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// findNamedChildOf returns the PID of a process whose command name is
// `name` and whose parent is `parentPID`. Returns 0 if none.
func findNamedChildOf(t *testing.T, parentPID int, name string) int {
	t.Helper()
	out, err := exec.Command("pgrep", "-P", strconv.Itoa(parentPID), name).CombinedOutput()
	if err != nil {
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
