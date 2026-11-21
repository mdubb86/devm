package main

import (
	"os/exec"
	"syscall"
	"testing"

	"github.com/mdubb86/devm/internal/schema"

	"github.com/stretchr/testify/assert"
)

func TestResolveRunFunction_UnknownFunction_ErrorListsAvailable(t *testing.T) {
	err := resolveRunFunction([]string{"install", "startup"}, "deploy", "/Users/me/proj", true)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), `function "deploy" not found`)
	assert.Contains(t, err.Error(), "install")
	assert.Contains(t, err.Error(), "startup")
}

func TestResolveRunFunction_KnownFunction_OK(t *testing.T) {
	err := resolveRunFunction([]string{"install", "deploy"}, "deploy", "/Users/me/proj", true)
	assert.NoError(t, err)
}

func TestResolveRunFunction_NoDevmSh_Error(t *testing.T) {
	err := resolveRunFunction(nil, "deploy", "/Users/me/proj", false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no devm.sh")
	assert.Contains(t, err.Error(), "/Users/me/proj")
}

func TestResolveRunFunction_DevmShExists_NoFunctions_DistinctError(t *testing.T) {
	// devm.sh exists (comments-only, variables-only, etc.) but Parse
	// found zero top-level functions. Must not report "no devm.sh".
	err := resolveRunFunction(nil, "deploy", "/Users/me/proj", true)
	assert.Error(t, err)
	assert.NotContains(t, err.Error(), "no devm.sh",
		"empty function set is NOT the same as missing file")
	assert.Contains(t, err.Error(), "no functions")
	assert.Contains(t, err.Error(), "/Users/me/proj")
}

func TestBuildBashArgs_InsertsDoubleDashBeforeFnArgs(t *testing.T) {
	// Without `--`, bash's own option parser eats a leading `-arg` and
	// either errors or prints its own help instead of running the user's
	// function.
	args := buildBashArgs([]string{"--debug", "val"})
	assert.Equal(t, []string{"-s", "--", "--debug", "val"}, args)
}

func TestBuildBashArgs_NoFnArgs_StillTerminatesOptions(t *testing.T) {
	args := buildBashArgs(nil)
	assert.Equal(t, []string{"-s", "--"}, args)
}

func TestExitCodeFromExitError_SignalDeath_Is128PlusSignal(t *testing.T) {
	// `exit 130` would make ProcessState.ExitCode() return 130 directly;
	// we need a REAL signal death to exercise the WaitStatus path.
	cmd := exec.Command("bash", "-c", "kill -INT $$; sleep 5")
	err := cmd.Run()
	ee, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v", err, err)
	}
	// Sanity: this IS the signal-death path (ExitCode is -1).
	assert.Equal(t, -1, ee.ExitCode(),
		"precondition: signal death returns ExitCode()=-1 (the 255-flattening bug)")
	assert.Equal(t, 128+int(syscall.SIGINT), exitCodeFromExitError(ee),
		"signal death must map to 128+N (130 for SIGINT), not -1/255")
}

func TestExitCodeFromExitError_NormalExit_PassesThrough(t *testing.T) {
	cmd := exec.Command("bash", "-c", "exit 7")
	err := cmd.Run()
	ee, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v", err, err)
	}
	assert.Equal(t, 7, exitCodeFromExitError(ee))
}

func TestBuildHostEnv_IncludesDEVMSIDEHostAndWorkspace(t *testing.T) {
	cfg := schema.Config{
		Env: map[string]schema.EnvValue{
			"FOO":          {Literal: "bar"},
			"SECRET_TOKEN": {Secret: &schema.SecretRef{Name: "api_token"}},
		},
	}
	env := buildHostEnv(cfg, "/Users/me/proj")
	assert.Equal(t, "host", env["DEVM_SIDE"])
	assert.Equal(t, "/Users/me/proj", env["WORKSPACE"])
	assert.Equal(t, "bar", env["FOO"])
	_, hasSecret := env["SECRET_TOKEN"]
	assert.False(t, hasSecret, "secret entries must be dropped from host script env")
}
