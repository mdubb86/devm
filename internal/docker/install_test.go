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
		"/usr/local/bin/devm-runc-shim",
		"/etc/docker/daemon.json",
		`"default-runtime": "devm"`,
		`"path": "/usr/local/bin/devm-runc-shim"`,
		"nft add rule inet devm_filter user_output ip daddr 172.16.0.0/12 accept",
		"systemctl daemon-reload",
		"systemctl restart docker",
		"test -x /usr/bin/runc",
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
