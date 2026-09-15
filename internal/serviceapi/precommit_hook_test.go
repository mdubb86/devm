package serviceapi

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// precommitFakeResponse is the canned reply for one script invocation.
type precommitFakeResponse struct {
	stdout   string
	stderr   string
	exitCode int
	err      error
}

// precommitFakeExec records every script it was asked to run and
// returns a canned response keyed by a substring match against the
// script text — the real GuestExec takes an opaque script string
// (executed via `bash -s` in the guest), so scripts can't be matched
// by exact argv the way an argv-based exec fake would. Distinct from
// the single-shot fakeGuestExec in mutagen_cold_start_test.go, which
// InstallPreCommitHook's multi-step check-then-install flow needs more
// than one canned response from.
type precommitFakeExec struct {
	// responses maps a substring to match against the script → the
	// response to return. Checked in order; first match wins.
	responses []struct {
		match    string
		response precommitFakeResponse
	}
	scripts []string
}

func (f *precommitFakeExec) on(match string, resp precommitFakeResponse) {
	f.responses = append(f.responses, struct {
		match    string
		response precommitFakeResponse
	}{match, resp})
}

func (f *precommitFakeExec) run(script string) (stdout, stderr string, exitCode int, err error) {
	f.scripts = append(f.scripts, script)
	for _, r := range f.responses {
		if strings.Contains(script, r.match) {
			return r.response.stdout, r.response.stderr, r.response.exitCode, r.response.err
		}
	}
	return "", "", 0, nil
}

func TestInstallPreCommitHook_FreshRepo_Installs(t *testing.T) {
	f := &precommitFakeExec{}
	f.on("git -C '/home/devm/proj' rev-parse --git-common-dir", precommitFakeResponse{stdout: "/home/devm/proj/.git\n"})
	f.on("test -e '/home/devm/proj/.git/hooks/pre-commit'", precommitFakeResponse{exitCode: 1})

	err := InstallPreCommitHook(GuestExec(f.run), "proj")
	require.NoError(t, err)

	var installScript string
	for _, s := range f.scripts {
		if strings.Contains(s, ".git/hooks/pre-commit.tmp") && strings.Contains(s, preCommitHookMarker) {
			installScript = s
		}
	}
	require.NotEmpty(t, installScript, "must write the hook body to a .tmp file; scripts=%v", f.scripts)

	// The install script itself (heredoc + chmod + mv) must be valid
	// shell, not just the hook body it embeds.
	if _, err := exec.LookPath("sh"); err == nil {
		path := filepath.Join(t.TempDir(), "install.sh")
		require.NoError(t, os.WriteFile(path, []byte(installScript), 0o755))
		out, err := exec.Command("sh", "-n", path).CombinedOutput()
		require.NoError(t, err, "sh -n rejected the install script: %s", out)
	}
}

func TestInstallPreCommitHook_ForeignHook_Skipped(t *testing.T) {
	f := &precommitFakeExec{}
	f.on("git -C '/home/devm/proj' rev-parse --git-common-dir", precommitFakeResponse{stdout: "/home/devm/proj/.git\n"})
	f.on("test -e '/home/devm/proj/.git/hooks/pre-commit'", precommitFakeResponse{exitCode: 0})
	f.on("head -n 2 '/home/devm/proj/.git/hooks/pre-commit'", precommitFakeResponse{stdout: "#!/bin/sh\n# Husky pre-commit\n"})

	err := InstallPreCommitHook(GuestExec(f.run), "proj")
	require.NoError(t, err)

	for _, s := range f.scripts {
		if strings.Contains(s, ".tmp") {
			t.Fatalf("must not write when a foreign hook exists; scripts=%v", f.scripts)
		}
	}
}

func TestInstallPreCommitHook_ManagedHook_Overwritten(t *testing.T) {
	f := &precommitFakeExec{}
	f.on("git -C '/home/devm/proj' rev-parse --git-common-dir", precommitFakeResponse{stdout: "/home/devm/proj/.git\n"})
	f.on("test -e '/home/devm/proj/.git/hooks/pre-commit'", precommitFakeResponse{exitCode: 0})
	f.on("head -n 2 '/home/devm/proj/.git/hooks/pre-commit'", precommitFakeResponse{stdout: fmt.Sprintf("#!/bin/sh\n%s\n", preCommitHookMarker)})

	err := InstallPreCommitHook(GuestExec(f.run), "proj")
	require.NoError(t, err)

	rewrote := false
	for _, s := range f.scripts {
		if strings.Contains(s, ".git/hooks/pre-commit.tmp") && strings.Contains(s, preCommitHookMarker) {
			rewrote = true
		}
	}
	assert.True(t, rewrote, "managed hook must be overwritten")
}

func TestInstallPreCommitHook_RelativeGitCommonDir_ResolvedAgainstWorkDir(t *testing.T) {
	f := &precommitFakeExec{}
	// Some git layouts return a --git-common-dir relative to workDir.
	f.on("git -C '/home/devm/proj' rev-parse --git-common-dir", precommitFakeResponse{stdout: ".git\n"})
	f.on("test -e '/home/devm/proj/.git/hooks/pre-commit'", precommitFakeResponse{exitCode: 1})

	err := InstallPreCommitHook(GuestExec(f.run), "proj")
	require.NoError(t, err)

	found := false
	for _, s := range f.scripts {
		if strings.Contains(s, "/home/devm/proj/.git/hooks/pre-commit.tmp") {
			found = true
		}
	}
	assert.True(t, found, "relative git-common-dir must resolve against workDir; scripts=%v", f.scripts)
}

func TestInstallPreCommitHook_GitCommonDirFails_ReturnsError(t *testing.T) {
	f := &precommitFakeExec{}
	f.on("git -C '/home/devm/proj' rev-parse --git-common-dir", precommitFakeResponse{exitCode: 128, stderr: "fatal: not a git repository"})

	err := InstallPreCommitHook(GuestExec(f.run), "proj")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a git repository")
}

func TestInstallPreCommitHook_HookBodyContainsExpectedText(t *testing.T) {
	lines := strings.SplitN(preCommitHookBody, "\n", 3)
	require.GreaterOrEqual(t, len(lines), 2, "hook body must have at least 2 lines")
	assert.Equal(t, preCommitHookMarker, lines[1], "marker must be on line 2")

	assert.Contains(t, preCommitHookBody, "devm.yaml")
	assert.Contains(t, preCommitHookBody, "devm.me.yaml")
	assert.Contains(t, preCommitHookBody, "--no-verify")
	assert.Contains(t, preCommitHookBody, "propose")
}

// TestInstallPreCommitHook_HookBodyIsValidShellSyntax runs `sh -n`
// (parse-only, no execution) over the exact bytes InstallPreCommitHook
// writes to the guest, catching a broken heredoc/quoting bug that a
// Contains() assertion on the constant text cannot.
func TestInstallPreCommitHook_HookBodyIsValidShellSyntax(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on PATH")
	}
	path := filepath.Join(t.TempDir(), "pre-commit")
	require.NoError(t, os.WriteFile(path, []byte(preCommitHookBody), 0o755))

	out, err := exec.Command("sh", "-n", path).CombinedOutput()
	require.NoError(t, err, "sh -n rejected the hook body: %s", out)
}
