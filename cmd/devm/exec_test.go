package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestExecCmd_StripsLeadingDashDash proves `devm exec -- ls` reaches
// the args processor as ["ls"] not ["--", "ls"] — otherwise the
// guest sees `--` as the command and errors out with
// "exec: --: not found" (buzztrack repro, v0.9.3 feedback log).
func TestExecCmd_StripsLeadingDashDash(t *testing.T) {
	got := stripLeadingDashDash([]string{"--", "ls", "-la"})
	assert.Equal(t, []string{"ls", "-la"}, got)
}

func TestExecCmd_StripLeadingDashDash_NoOpWhenAbsent(t *testing.T) {
	got := stripLeadingDashDash([]string{"ls", "-la"})
	assert.Equal(t, []string{"ls", "-la"}, got)
}

func TestExecCmd_StripLeadingDashDash_OnlyStripsLeading(t *testing.T) {
	// A `--` mid-args is a legitimate arg to the guest command
	// (e.g. `devm exec -- sh -c "echo -- foo"`). We only strip the
	// first `--` that immediately follows `exec`, which cobra
	// presents as the first positional arg.
	got := stripLeadingDashDash([]string{"sh", "-c", "echo -- foo"})
	assert.Equal(t, []string{"sh", "-c", "echo -- foo"}, got)
}

// TestExecCmd_HelpFlagPrintsUsage proves `devm exec --help` prints
// the command's Long help instead of trying to run `--help` in the
// guest (v0.9.3 feedback: "prints nothing" — the guest exec ran
// `--help` and failed silently).
func TestExecCmd_HelpFlagPrintsUsage(t *testing.T) {
	var buf bytes.Buffer
	execCmd.SetOut(&buf)
	t.Cleanup(func() { execCmd.SetOut(nil) })

	handled := handleExecHelpFlag([]string{"--help"}, execCmd)
	assert.True(t, handled, "handleExecHelpFlag must claim --help")
	assert.NotEmpty(t, buf.String(), "expected help text to be written")
	assert.Contains(t, buf.String(), "Runs COMMAND inside the sandbox")
}

func TestExecCmd_HelpFlag_NotClaimedWhenMoreArgs(t *testing.T) {
	handled := handleExecHelpFlag([]string{"--help", "extra"}, execCmd)
	assert.False(t, handled, "with additional args, --help is passed to guest")
}
