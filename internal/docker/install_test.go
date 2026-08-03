package docker

import (
	"strings"
	"testing"
)

func TestInstallScript_ContainsRequiredPieces(t *testing.T) {
	script := InstallScript()
	musts := []string{
		"set -o pipefail",
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
		// networkMode=host in the OCI worker so RUN steps use the guest's
		// netns, DNS resolves via iron-proxy's dnsmasq, HTTPS is MITM'd —
		// otherwise builds fail with "Temporary failure resolving …".
		// Key is `networkMode` (not `network`) per buildkit v0.28.1's
		// NetworkConfig struct — wrong keys silently no-op.
		`networkMode = "host"`,
		// [dns] nameservers = ["127.0.0.1"] pins buildkit's per-container
		// resolv.conf at the guest's dnsmasq (reachable via host netns),
		// bypassing buildkit's default "127.x.x.x → 8.8.8.8" rewrite that
		// otherwise silently breaks DNS resolution during RUN steps.
		`[dns]`,
		`nameservers = ["127.0.0.1"]`,
		// Inline upstream systemd units (tarball ships bin/ only).
		"tee /etc/systemd/system/buildkit.service",
		"tee /etc/systemd/system/buildkit.socket",
		"ExecStart=/usr/local/bin/buildkitd --addr fd://",
		"ListenStream=%t/buildkit/buildkitd.sock",
		// SocketMode=0666 so devm user can dial without depending on
		// docker group membership (usermod -aG changes /etc/group but
		// the live SSH session's process creds don't pick it up).
		"SocketMode=0666",
		"systemctl enable --now buildkit.socket buildkit.service",
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

func TestInstallScript_PipefailFollowsFailFast(t *testing.T) {
	// `set -o pipefail` must immediately follow `set -e` so a failed
	// curl in a `curl | tar` or `curl | sh` pipe (steps 1 and 7) can't
	// be masked by the downstream command exiting 0 on bad/empty input.
	lines := strings.Split(strings.TrimSpace(InstallScript()), "\n")
	if len(lines) < 2 || lines[0] != "set -e" || lines[1] != "set -o pipefail" {
		got := lines
		if len(got) > 2 {
			got = got[:2]
		}
		t.Errorf("InstallScript's first two lines must be `set -e` then `set -o pipefail`, got: %v", got)
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
