package docker

import (
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
		"systemctl daemon-reload",
		"systemctl restart docker",
		"test -x /usr/bin/runc",
		// Shim is delivered via the devmbundle; the install script's own guard is a `test -x`.
		"test -x /usr/local/bin/devm-runc-shim",
		// Buildkit deploy (new):
		"BUILDKIT_VERSION=v0.28.1",
		"github.com/moby/buildkit/releases/download/${BUILDKIT_VERSION}/buildkit-${BUILDKIT_VERSION}.linux-arm64.tar.gz",
		"/etc/buildkit/buildkitd.toml",
		`binary = "/usr/local/bin/devm-runc-shim"`,
		"/usr/local/examples/systemd/system/buildkit.service",
		"/usr/local/examples/systemd/system/buildkit.socket",
		"systemctl enable --now buildkit.socket",
		"docker buildx inspect devm",
		"docker buildx create",
		"--driver remote",
		"--name devm",
		"unix:///run/buildkit/buildkitd.sock",
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

func TestInstallScript_BuildkitBlockIsIdempotent(t *testing.T) {
	// The `docker buildx create devm` step must only fire when the
	// builder doesn't already exist, so re-running the install script
	// (a routine part of `devm reconcile`) doesn't error on a second
	// `create devm`.
	script := InstallScript()
	// Guard shape: `if ! docker buildx inspect devm …; then … create …; fi`
	if !strings.Contains(script, "if ! docker buildx inspect devm") {
		t.Errorf("buildx create must be guarded by `if ! docker buildx inspect devm`; script:\n%s", script)
	}
}
