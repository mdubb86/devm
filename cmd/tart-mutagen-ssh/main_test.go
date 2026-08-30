package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSSHArgs_ExtractsVMNameAndCommand(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantVM  string
		wantCmd []string
	}{
		{
			name:    "typical mutagen invocation",
			args:    []string{"-oConnectTimeout=5", "-oServerAliveInterval=10", "-oServerAliveCountMax=1", "devm@devm-proj123", ".mutagen/agents/0.18.1/mutagen-agent", "synchronizer", "--log-level=debug"},
			wantVM:  "proj123",
			wantCmd: []string{".mutagen/agents/0.18.1/mutagen-agent", "synchronizer", "--log-level=debug"},
		},
		{
			name:    "bare host without user",
			args:    []string{"-oConnectTimeout=5", "devm-proj123", "mutagen-agent"},
			wantVM:  "proj123",
			wantCmd: []string{"mutagen-agent"},
		},
		{
			name:    "no -o flags",
			args:    []string{"devm@devm-foo", "cmd"},
			wantVM:  "foo",
			wantCmd: []string{"cmd"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			vm, cmd, err := parseSSHArgs(c.args)
			require.NoError(t, err)
			assert.Equal(t, c.wantVM, vm)
			assert.Equal(t, c.wantCmd, cmd)
		})
	}
}

func TestParseSSHArgs_ErrorsOnMissingHost(t *testing.T) {
	_, _, err := parseSSHArgs([]string{"-oConnectTimeout=5"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no host")
}

func TestParseSSHArgs_ErrorsOnMissingCommand(t *testing.T) {
	_, _, err := parseSSHArgs([]string{"devm@devm-foo"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no command")
}

func TestParseSSHArgs_HostWithoutDevmPrefix(t *testing.T) {
	// Defensive: if mutagen ever hits a host that isn't "devm-<vm>", we
	// still return a name (whatever's after @, unmodified). Wrong VM name
	// means tart exec will fail loudly, which is what we want.
	vm, _, err := parseSSHArgs([]string{"user@some-other-host", "cmd"})
	require.NoError(t, err)
	assert.Equal(t, "some-other-host", vm)
}

func TestDispatchName_ClassifiesByArgv0(t *testing.T) {
	assert.Equal(t, "ssh", dispatchName("/path/to/ssh"))
	assert.Equal(t, "scp", dispatchName("/path/to/scp"))
	assert.Equal(t, "ssh", dispatchName("ssh"))
	assert.Equal(t, "scp", dispatchName("scp"))
	// Anything else defaults to ssh — mutagen always looks up "ssh" or
	// "scp" specifically, so this fallback only affects developer testing
	// (e.g. `go run ./cmd/tart-mutagen-ssh ...`).
	assert.Equal(t, "ssh", dispatchName("tart-mutagen-ssh"))
}

// End-to-end shim behavior test: build the binary, invoke it with a fake
// `tart` in PATH, verify tart got the right argv. Uses a stub tart
// script so no VM is required.
func TestShim_InvokesTartExecWithCmd(t *testing.T) {
	dir := t.TempDir()

	// Build the shim.
	shim := filepath.Join(dir, "ssh")
	build := exec.Command("go", "build", "-o", shim, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build shim: %v\n%s", err, out)
	}

	// Stub `tart` binary — records its argv to a log file.
	logPath := filepath.Join(dir, "tart-argv.log")
	tartStub := filepath.Join(dir, "tart")
	stubScript := `#!/bin/bash
printf '%s\n' "$@" > "` + logPath + `"
exit 0
`
	require.NoError(t, os.WriteFile(tartStub, []byte(stubScript), 0o755))

	cmd := exec.Command(shim,
		"-oConnectTimeout=5",
		"-oServerAliveInterval=10",
		"-oServerAliveCountMax=1",
		"devm@devm-testvm",
		".mutagen/agents/0.18.1/mutagen-agent",
		"synchronizer",
		"--log-level=debug",
	)
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("shim run: %v\n%s", err, out)
	}

	body, err := os.ReadFile(logPath)
	require.NoError(t, err)
	got := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	assert.Equal(t, []string{
		"exec", "testvm",
		"/home/devm/.mutagen/agents/0.18.1/mutagen-agent",
		"synchronizer", "--log-level=debug",
	}, got)
}

func TestShim_LeavesNonMutagenPathsAlone(t *testing.T) {
	dir := t.TempDir()
	shim := filepath.Join(dir, "ssh")
	build := exec.Command("go", "build", "-o", shim, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build shim: %v\n%s", err, out)
	}
	logPath := filepath.Join(dir, "tart-argv.log")
	tartStub := filepath.Join(dir, "tart")
	stubScript := `#!/bin/bash
printf '%s\n' "$@" > "` + logPath + `"
exit 0
`
	require.NoError(t, os.WriteFile(tartStub, []byte(stubScript), 0o755))

	cmd := exec.Command(shim, "devm@devm-testvm", "/usr/bin/whoami")
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("shim run: %v\n%s", err, out)
	}
	body, err := os.ReadFile(logPath)
	require.NoError(t, err)
	got := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	assert.Equal(t, []string{"exec", "testvm", "/usr/bin/whoami"}, got,
		"commands not starting with .mutagen/ must pass through unchanged")
}

func TestShim_ScpInvocationErrors(t *testing.T) {
	dir := t.TempDir()
	shim := filepath.Join(dir, "scp")
	build := exec.Command("go", "build", "-o", shim, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build shim: %v\n%s", err, out)
	}
	cmd := exec.Command(shim, "src", "dest")
	out, err := cmd.CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(out), "scp invocation not supported")
	assert.Equal(t, 2, cmd.ProcessState.ExitCode())
}

func TestShim_PropagatesChildExitCode(t *testing.T) {
	dir := t.TempDir()
	shim := filepath.Join(dir, "ssh")
	build := exec.Command("go", "build", "-o", shim, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build shim: %v\n%s", err, out)
	}
	tartStub := filepath.Join(dir, "tart")
	require.NoError(t, os.WriteFile(tartStub, []byte("#!/bin/bash\nexit 42\n"), 0o755))

	cmd := exec.Command(shim, "-oConnectTimeout=5", "devm@devm-vm", "cmd")
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	_ = cmd.Run()
	assert.Equal(t, 42, cmd.ProcessState.ExitCode())
}
