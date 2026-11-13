package render

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderInstallScript_SubstitutesVersion(t *testing.T) {
	body, err := RenderInstallScript("0.18.1")
	require.NoError(t, err)
	s := string(body)
	assert.Contains(t, s, "/home/devm/.mutagen/agents/0.18.1")
	assert.NotContains(t, s, "{{.MutagenVersion}}",
		"template placeholder must be substituted")
}

func TestRenderInstallScript_PreservesLiterals(t *testing.T) {
	// Every non-templated part of install.sh should render byte-identical
	// to the untemplated version. Assert on a few load-bearing literals.
	body, err := RenderInstallScript("0.18.1")
	require.NoError(t, err)
	s := string(body)
	assert.Contains(t, s, "update-ca-certificates --fresh",
		"CA install block must survive templating")
	assert.Contains(t, s, "for f in /opt/devm/bin/*",
		"bin loop must survive templating")
	assert.Contains(t, s, "systemctl unmask ssh",
		"ssh unmask must survive templating")
}

func TestRenderInstallScript_EnablesAndRestartsGdevmServe(t *testing.T) {
	// Pins that a cold-start (and any re-run of install.sh, which is
	// also the bundle-refresh / reconcile path — see
	// devmbundle.GuestInstallScript) reloads systemd, enables
	// gdevm-serve, and restarts it so a refreshed binary is picked up.
	body, err := RenderInstallScript("0.18.1")
	require.NoError(t, err)
	s := string(body)
	assert.Contains(t, s, "systemctl daemon-reload")
	assert.Contains(t, s, "systemctl enable gdevm-serve.service")
	assert.Contains(t, s, "systemctl restart gdevm-serve.service")
}

func TestRenderInstallScript_RestartsGdevmServeAfterBinInstalled(t *testing.T) {
	// gdevm-serve's ExecStart points at /usr/local/bin/gdevm, which the
	// "for f in /opt/devm/bin/*" loop is what installs. On a cold VM
	// boot nothing else puts a binary there first, so the restart must
	// come strictly after that loop — restarting before it exists makes
	// systemctl fail and, under `set -e`, aborts the rest of install.sh
	// (SSH material, Docker shims, Mutagen agent pre-install).
	body, err := RenderInstallScript("0.18.1")
	require.NoError(t, err)
	s := string(body)

	binLoopIdx := strings.Index(s, "for f in /opt/devm/bin/*")
	require.NotEqual(t, -1, binLoopIdx, "bin install loop must be present")

	restartIdx := strings.Index(s, "systemctl restart gdevm-serve.service")
	require.NotEqual(t, -1, restartIdx, "gdevm-serve restart must be present")

	assert.Less(t, binLoopIdx, restartIdx,
		"gdevm-serve restart must run after the /opt/devm/bin/* install loop, not before")
}

func TestRenderInstallScript_RejectsEmptyVersion(t *testing.T) {
	_, err := RenderInstallScript("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutagen version")
}

func TestRenderInstallScript_HasClientSSHSection(t *testing.T) {
	body, err := RenderInstallScript("0.18.1")
	require.NoError(t, err)
	s := string(body)
	for _, want := range []string{
		"client-SSH material",
		"install -m 0600 -o devm -g devm /opt/devm/ssh-client/config /home/devm/.ssh/config",
		"install -m 0600 -o devm -g devm /opt/devm/ssh-client/devm-jump-id /home/devm/.ssh/devm-jump-id",
		"systemctl enable --now devm-ssh-agent-bridge.service",
		"systemctl disable --now devm-ssh-agent-bridge.service",
	} {
		assert.Contains(t, s, want, "install.sh missing %q", want)
	}

	// The if-branch installs the unit and then enables it — systemd must
	// have reloaded first or `enable` picks up the old path. The else-
	// branch removes the unit file and must reload after to drop the
	// cached path reference, or `systemctl status` keeps pointing at a
	// file that no longer exists.
	installUnitIdx := strings.Index(s, "install -m 0644 /opt/devm/ssh-client/devm-ssh-agent-bridge.service /etc/systemd/system/")
	require.NotEqual(t, -1, installUnitIdx, "unit install step must be present")
	enableIdx := strings.Index(s, "systemctl enable --now devm-ssh-agent-bridge.service")
	require.NotEqual(t, -1, enableIdx, "enable step must be present")
	reloadBeforeEnable := strings.Index(s[installUnitIdx:enableIdx], "systemctl daemon-reload")
	assert.NotEqual(t, -1, reloadBeforeEnable,
		"daemon-reload must run between install and enable (if-branch)")

	rmIdx := strings.Index(s, "rm -f /etc/systemd/system/devm-ssh-agent-bridge.service")
	require.NotEqual(t, -1, rmIdx, "else-branch rm step must be present")
	// Pin the reload to the else-branch itself (before the closing `fi`
	// of the client-SSH block) so we don't silently match a later
	// daemon-reload from the gdevm-serve or filestash sections.
	elseEnd := strings.Index(s[rmIdx:], "\nfi\n")
	require.NotEqual(t, -1, elseEnd, "else-branch close must follow the rm")
	elseBranch := s[rmIdx : rmIdx+elseEnd]
	assert.Contains(t, elseBranch, "systemctl daemon-reload",
		"daemon-reload must run inside the else-branch, after rm -f")
}

func TestRenderInstallScript_DifferentVersionsProduceDifferentOutput(t *testing.T) {
	a, err := RenderInstallScript("0.18.1")
	require.NoError(t, err)
	b, err := RenderInstallScript("0.19.0")
	require.NoError(t, err)
	assert.NotEqual(t, a, b, "version bump should change rendered output")
	assert.Contains(t, string(a), "0.18.1")
	assert.Contains(t, string(b), "0.19.0")
	assert.NotContains(t, string(a), "0.19.0")
	assert.NotContains(t, string(b), "0.18.1")
}
