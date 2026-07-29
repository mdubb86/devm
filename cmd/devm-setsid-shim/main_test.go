package main

import (
	"bufio"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestShim_ChildRunsInNewSession proves the shim's core promise:
// the target process is in a different session (SID) than the shim
// itself. Without Setsid, both would share the caller's session
// and launchctl bootout of a parent daemon would kill both.
//
// Session id is checked via syscall.Getsid rather than `ps`: on this
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

	childSID, err := syscall.Getsid(childPID)
	require.NoError(t, err)

	ownSID, err := syscall.Getsid(0)
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
