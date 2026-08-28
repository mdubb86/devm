package serviceapi

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeGuestExec captures the script it's asked to run and returns the
// canned stdout/stderr/exitCode/err without touching a real guest.
func fakeGuestExec(stdout, stderr string, exitCode int, err error) (exec GuestExec, captured *string) {
	captured = new(string)
	return func(script string) (string, string, int, error) {
		*captured = script
		return stdout, stderr, exitCode, err
	}, captured
}

func TestCloneRepoInGuest_HappyPath(t *testing.T) {
	exec, captured := fakeGuestExec("", "", 0, nil)

	req := CloneRequest{
		URL:             "https://github.com/example/repo.git",
		SecretName:      "gh_token",
		GuestTargetPath: "/home/devm/work/repo",
		IronProxyURL:    "http://127.0.0.1:5555",
		GuestCACertPath: "/etc/ssl/certs/devm-iron-proxy-ca.crt",
	}

	err := CloneRepoInGuest(exec, req)
	require.NoError(t, err)

	script := *captured
	assert.Contains(t, script, "sudo -u devm bash -c")
	assert.Contains(t, script, "export HTTP_PROXY=http://127.0.0.1:5555")
	assert.Contains(t, script, "export HTTPS_PROXY=http://127.0.0.1:5555")
	assert.Contains(t, script, "export GIT_SSL_CAINFO=/etc/ssl/certs/devm-iron-proxy-ca.crt")
	assert.Contains(t, script, "git clone")
	assert.Contains(t, script, "https://github.com/example/repo.git")
	assert.Contains(t, script, "/home/devm/work/repo")

	wantBlob := base64.StdEncoding.EncodeToString([]byte("x-access-token:__DEVM_SECRET_gh_token__"))
	assert.Contains(t, script, "Authorization: Basic "+wantBlob)
}

// runGuestScriptForReal executes the script CloneRepoInGuest built through
// a real bash, with fake `sudo` (drops "-u <user>" and execs the rest) and
// fake `git` (records its argv instead of cloning) on PATH. This proves the
// generated script is not just string-similar to the expected shape but
// actually parses and behaves the way a POSIX shell would when handed to
// `tart exec`.
func runGuestScriptForReal(t *testing.T, script string) (gitArgv []string) {
	t.Helper()
	fakeBinDir := t.TempDir()
	argvFile := filepath.Join(fakeBinDir, "argv.txt")

	require.NoError(t, os.WriteFile(filepath.Join(fakeBinDir, "sudo"), []byte(
		"#!/bin/sh\nif [ \"$1\" = \"-u\" ]; then shift 2; fi\nexec \"$@\"\n"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(fakeBinDir, "git"), []byte(
		"#!/bin/sh\nprintf '%s\\n' \"$@\" > "+argvFile+"\n"), 0o755))

	cmd := exec.Command("bash", "-c", script)
	cmd.Env = append(os.Environ(), "PATH="+fakeBinDir+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "generated script must be valid, executable shell: %s", out)

	raw, err := os.ReadFile(argvFile)
	require.NoError(t, err, "fake git was never invoked")
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	return lines
}

func TestCloneRepoInGuest_QuotesURLAndPath(t *testing.T) {
	pwnMarker := filepath.Join(t.TempDir(), "pwn")
	execFn, captured := fakeGuestExec("", "", 0, nil)

	maliciousURL := "https://foo/$(touch " + pwnMarker + ").git"
	maliciousTarget := "/home/devm/work/repo; touch " + pwnMarker

	req := CloneRequest{
		URL:             maliciousURL,
		SecretName:      "gh_token",
		GuestTargetPath: maliciousTarget,
		IronProxyURL:    "http://127.0.0.1:5555",
		GuestCACertPath: "/etc/ssl/certs/devm-iron-proxy-ca.crt",
	}

	err := CloneRepoInGuest(execFn, req)
	require.NoError(t, err)

	script := *captured
	// The literal, unexpanded text must be present in the script...
	assert.Contains(t, script, "$(touch")

	// ...but only as an inert argument: executing the real script for
	// real must not run the embedded command substitution or the
	// semicolon-appended command.
	argv := runGuestScriptForReal(t, script)
	require.Len(t, argv, 8, "clone argv: clone --quiet -c <http.proxy> -c <header> <url> <target>")
	assert.Equal(t, maliciousURL, argv[6], "git must receive the URL literally, unexpanded")
	assert.Equal(t, maliciousTarget, argv[7], "git must receive the target path literally, unexpanded")

	_, statErr := os.Stat(pwnMarker)
	assert.True(t, os.IsNotExist(statErr), "injected command must never execute; marker file must not exist")
}

func TestCloneRepoInGuest_NonZeroExit(t *testing.T) {
	execFn, _ := fakeGuestExec("", "fatal: repository not found", 128, nil)

	req := CloneRequest{
		URL:             "https://github.com/example/missing.git",
		SecretName:      "gh_token",
		GuestTargetPath: "/home/devm/work/repo",
		IronProxyURL:    "http://127.0.0.1:5555",
		GuestCACertPath: "/etc/ssl/certs/devm-iron-proxy-ca.crt",
	}

	err := CloneRepoInGuest(execFn, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "git clone")
	assert.Contains(t, err.Error(), "fatal: repository not found")
}
