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
		// Shim is piped in over stdin before this script runs; the
		// install script's own guard is a `test -x`.
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

// recordingShell captures the sequence of ExecShell/PipeIntoShell
// calls Install() makes so tests can pin the ordering + payloads.
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

func (r *recordingShell) PipeIntoShell(_ context.Context, _ io.Writer, stdin io.Reader, script string) error {
	// Drain stdin so the caller's payload actually exists (paranoia).
	if _, err := io.Copy(io.Discard, stdin); err != nil {
		return err
	}
	switch {
	case strings.Contains(script, "devm-runc-shim"):
		r.events = append(r.events, "pipe:runc-shim")
	case strings.Contains(script, "/usr/local/bin/docker"):
		r.events = append(r.events, "pipe:docker-shim")
	default:
		r.events = append(r.events, "pipe:unknown")
	}
	return nil
}

// TestInstall_OrderingAndPayloads pins the three-stage sequence:
// runc-shim first (daemon.json refers to it before docker starts),
// then the install script (which runs get.docker.com and installs
// the real docker at /usr/bin/docker), then the docker-shim (which
// needs the real docker to already exist so its exec-forward chain
// works). Regressing this order would break either daemon startup
// (runc-shim missing) or the shim (nothing to exec-forward to).
func TestInstall_OrderingAndPayloads(t *testing.T) {
	sh := &recordingShell{}
	if err := Install(context.Background(), &bytes.Buffer{}, sh); err != nil {
		t.Fatalf("Install: %v", err)
	}
	want := []string{
		"pipe:runc-shim",
		"exec:install_script",
		"pipe:docker-shim",
	}
	if len(sh.events) != len(want) {
		t.Fatalf("events = %v, want %v", sh.events, want)
	}
	for i, w := range want {
		if sh.events[i] != w {
			t.Errorf("event[%d] = %q, want %q", i, sh.events[i], w)
		}
	}
}

// TestDockerShimInstallScript_GuardsRealDocker pins that the shim-
// install script refuses to overwrite /usr/local/bin/docker when
// /usr/bin/docker isn't there yet. Landing the shim without a real
// docker to forward to would exec-loop every invocation.
func TestDockerShimInstallScript_GuardsRealDocker(t *testing.T) {
	if !strings.Contains(dockerShimInstallScript, "test -x /usr/bin/docker") {
		t.Errorf("dockerShimInstallScript must guard on real docker existing, got:\n%s", dockerShimInstallScript)
	}
	if !strings.Contains(dockerShimInstallScript, "tee /usr/local/bin/docker") {
		t.Errorf("dockerShimInstallScript must write to /usr/local/bin/docker, got:\n%s", dockerShimInstallScript)
	}
	if !strings.Contains(dockerShimInstallScript, "chmod 0755 /usr/local/bin/docker") {
		t.Errorf("dockerShimInstallScript must chmod the shim executable, got:\n%s", dockerShimInstallScript)
	}
}
