package orchestrator

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/mdubb86/devm/internal/mutagen"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------- local fakes for runStartupCommandsWithCLI tests ----------

// scriptedMutagenCLI builds a *mutagen.CLI whose Exec dispatches on the
// mutagen subcommand (args[0] args[1]), recording every call it sees into
// a shared calls log alongside guestExec invocations — mirrors the
// scriptedCLI fake in internal/serviceapi/mutagen_sessions_test.go, kept
// local here since that one is unexported to its own package.
type scriptedMutagenCLI struct {
	listSessions []mutagen.SyncSession
	calls        *[]string // shared timeline with the fake guestExec below
}

func (s *scriptedMutagenCLI) build() *mutagen.CLI {
	return &mutagen.CLI{
		Binary: "mutagen",
		Exec: func(bin string, args []string, env []string) (string, string, int, error) {
			if len(args) >= 2 && args[0] == "sync" {
				switch args[1] {
				case "list":
					rows := make([]struct {
						Identifier string `json:"identifier"`
						Name       string `json:"name"`
						Status     string `json:"status"`
						Paused     bool   `json:"paused"`
					}, len(s.listSessions))
					for i, sess := range s.listSessions {
						rows[i] = struct {
							Identifier string `json:"identifier"`
							Name       string `json:"name"`
							Status     string `json:"status"`
							Paused     bool   `json:"paused"`
						}{sess.ID, sess.Name, sess.Status, sess.Paused}
					}
					b, _ := json.Marshal(rows)
					return string(b), "", 0, nil
				case "flush":
					*s.calls = append(*s.calls, "flush:"+args[2])
					return "", "", 0, nil
				}
			}
			return "", "", 0, fmt.Errorf("scriptedMutagenCLI: unhandled args %v", args)
		},
	}
}

// recordingGuestExec returns a serviceapi.GuestExec that appends "run:<n>"
// (n = 1-based call index) into the shared calls log for every invocation,
// so a test can interleave it with scriptedMutagenCLI's flush calls into
// one ordered timeline. failOnCall, if > 0, makes that call (1-based)
// return exitCode with stderr/stdout containing failMsg.
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

func TestRunStartupCommands_FlushBeforeExec(t *testing.T) {
	var calls []string
	sc := &scriptedMutagenCLI{
		listSessions: []mutagen.SyncSession{
			{ID: "sess-1", Name: "devm-x-repo1"},
			{ID: "sess-2", Name: "devm-x-repo2"},
		},
		calls: &calls,
	}
	cli := sc.build()
	exec := recordingGuestExec(&calls, 0, 0, "")

	startupCmds := []schema.StartupCommand{
		{Repo: "repo1", Name: "seed", GuestCwd: "/home/devm/repo1"},
		{Repo: "repo2", Name: "build", GuestCwd: "/home/devm/repo2"},
	}

	err := runStartupCommandsWithCLI(cli, exec, "x", startupCmds)
	require.NoError(t, err)

	// Every flush call must precede every run (guest-exec) call.
	lastFlush, firstRun := -1, -1
	for i, c := range calls {
		switch {
		case c == "flush:sess-1" || c == "flush:sess-2":
			lastFlush = i
		case firstRun == -1 && len(c) >= 4 && c[:4] == "run:":
			firstRun = i
		}
	}
	require.GreaterOrEqual(t, lastFlush, 0, "flush calls must be present")
	require.GreaterOrEqual(t, firstRun, 0, "run calls must be present")
	assert.Less(t, lastFlush, firstRun, "every SyncFlush call must precede every guest-exec call")

	// Both sessions flushed, both commands dispatched.
	assert.Contains(t, calls, "flush:sess-1")
	assert.Contains(t, calls, "flush:sess-2")
	assert.Contains(t, calls, "run:1")
	assert.Contains(t, calls, "run:2")
}

func TestRunStartupCommands_ExecFailurePropagates(t *testing.T) {
	var calls []string
	sc := &scriptedMutagenCLI{calls: &calls} // no sessions to flush
	cli := sc.build()
	exec := recordingGuestExec(&calls, 2, 42, "boom: something broke")

	startupCmds := []schema.StartupCommand{
		{Repo: "repo1", Name: "cmd1", GuestCwd: "/home/devm/repo1"},
		{Repo: "repo1", Name: "cmd2", GuestCwd: "/home/devm/repo1"},
		{Repo: "repo1", Name: "cmd3", GuestCwd: "/home/devm/repo1"},
	}

	err := runStartupCommandsWithCLI(cli, exec, "x", startupCmds)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repo1/cmd2")
	assert.Contains(t, err.Error(), "exit 42")

	// The third command must never run — dispatch stops at the first
	// failure.
	assert.NotContains(t, calls, "run:3")
}
