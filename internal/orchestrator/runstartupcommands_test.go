package orchestrator

import (
	"fmt"
	"testing"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------- local fakes for dispatchStartupCommands tests ----------

// recordingGuestExec returns a serviceapi.GuestExec that appends "run:<n>"
// (n = 1-based call index) into the shared calls log for every invocation.
// failOnCall, if > 0, makes that call (1-based) return exitCode with
// stderr/stdout containing failMsg.
func recordingGuestExec(calls *[]string, failOnCall int, exitCode int, failMsg string) serviceapi.GuestExec {
	n := 0
	return func(script string) (stdout, stderr string, code int, err error) {
		n++
		*calls = append(*calls, fmt.Sprintf("run:%d", n))
		if failOnCall > 0 && n == failOnCall {
			return "", failMsg, exitCode, nil
		}
		return "", "", 0, nil
	}
}

// TestDispatchStartupCommands_RunsEachCommandInOrder verifies Task 8:
// dispatchStartupCommands no longer flushes mutagen itself (that moved
// upstream to waitForInitialSyncFn, called once in provisionAndAttach
// before RunOpen) — it just dispatches every startup command's `run <name>`
// in order.
func TestDispatchStartupCommands_RunsEachCommandInOrder(t *testing.T) {
	var calls []string
	exec := recordingGuestExec(&calls, 0, 0, "")

	startupCmds := []schema.StartupCommand{
		{Repo: "repo1", Name: "seed", GuestCwd: "/home/devm/repo1"},
		{Repo: "repo2", Name: "build", GuestCwd: "/home/devm/repo2"},
	}

	err := dispatchStartupCommands(exec, "x", startupCmds)
	require.NoError(t, err)
	assert.Equal(t, []string{"run:1", "run:2"}, calls)
}

func TestDispatchStartupCommands_ExecFailurePropagates(t *testing.T) {
	var calls []string
	exec := recordingGuestExec(&calls, 2, 42, "boom: something broke")

	startupCmds := []schema.StartupCommand{
		{Repo: "repo1", Name: "cmd1", GuestCwd: "/home/devm/repo1"},
		{Repo: "repo1", Name: "cmd2", GuestCwd: "/home/devm/repo1"},
		{Repo: "repo1", Name: "cmd3", GuestCwd: "/home/devm/repo1"},
	}

	err := dispatchStartupCommands(exec, "x", startupCmds)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repo1/cmd2")
	assert.Contains(t, err.Error(), "exit 42")

	// The third command must never run — dispatch stops at the first
	// failure.
	assert.NotContains(t, calls, "run:3")
}
