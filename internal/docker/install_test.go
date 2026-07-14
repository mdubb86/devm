package docker

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

func TestInstallScript_ContainsRequiredPieces(t *testing.T) {
	script := InstallScript()
	musts := []string{
		"curl -fsSL https://get.docker.com | sh",
		"usermod -aG docker devm",
		"/etc/systemd/system/docker.service.d/override.conf",
		"chmod 666 /run/docker.sock",
		"/etc/docker/daemon.json",
		`"default-runtime": "devm"`,
		`"path": "/usr/local/bin/devm-runc-shim"`,
		"nft add rule inet devm_filter user_output ip daddr 172.16.0.0/12 accept",
		"systemctl daemon-reload",
		"systemctl restart docker",
		"test -x /usr/bin/runc",
		// Shim is delivered via the devmbundle; the install script's own guard is a `test -x`.
		"test -x /usr/local/bin/devm-runc-shim",
	}
	for _, m := range musts {
		if !strings.Contains(script, m) {
			t.Errorf("InstallScript missing required content %q", m)
		}
	}
}

func TestInstallScript_UsesFailFast(t *testing.T) {
	// `set -e` at the top so any subshell failure fails the step,
	// rather than the provisioner silently succeeding halfway through.
	if !strings.HasPrefix(strings.TrimSpace(InstallScript()), "set -e") {
		t.Errorf("InstallScript must begin with `set -e`, got:\n%s", InstallScript())
	}
}

// recordingShell captures the ExecShell calls Install() makes.
type recordingShell struct {
	events []string
}

func (r *recordingShell) ExecShell(_ context.Context, _ io.Writer, script string) error {
	// Compact the recorded event to first line so the assertion
	// stays readable; the exact identity of the InstallScript is
	// covered by TestInstallScript_ContainsRequiredPieces.
	label := "exec"
	if strings.Contains(script, "get.docker.com") {
		label = "exec:install_script"
	}
	r.events = append(r.events, label)
	return nil
}

// TestInstall_CallsExecShell pins that Install runs the install script via ExecShell.
func TestInstall_CallsExecShell(t *testing.T) {
	sh := &recordingShell{}
	if err := Install(context.Background(), &bytes.Buffer{}, sh); err != nil {
		t.Fatalf("Install: %v", err)
	}
	want := []string{"exec:install_script"}
	if len(sh.events) != len(want) {
		t.Fatalf("events = %v, want %v", sh.events, want)
	}
	if sh.events[0] != want[0] {
		t.Errorf("event[0] = %q, want %q", sh.events[0], want[0])
	}
}
