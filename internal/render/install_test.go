package render

import (
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

func TestRenderInstallScript_SubstitutesVersion(t *testing.T) {
	body, err := RenderInstallScript("0.18.1", "myproj")
	require.NoError(t, err)
	s := string(body)
	assert.Contains(t, s, "/home/devm/.mutagen/agents/0.18.1")
	assert.NotContains(t, s, "{{.MutagenVersion}}",
		"template placeholder must be substituted")
}

func TestRenderInstallScript_PreservesLiterals(t *testing.T) {
	// Every non-templated part of install.sh should render byte-identical
	// to the untemplated version. Assert on a few load-bearing literals.
	body, err := RenderInstallScript("0.18.1", "myproj")
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
	body, err := RenderInstallScript("0.18.1", "myproj")
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
	body, err := RenderInstallScript("0.18.1", "myproj")
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
	_, err := RenderInstallScript("", "myproj")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutagen version")
}

func TestRenderInstallScript_RejectsEmptyProjectName(t *testing.T) {
	_, err := RenderInstallScript("0.18.1", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "project name")
}

func TestRenderInstallScript_DifferentVersionsProduceDifferentOutput(t *testing.T) {
	a, err := RenderInstallScript("0.18.1", "myproj")
	require.NoError(t, err)
	b, err := RenderInstallScript("0.19.0", "myproj")
	require.NoError(t, err)
	assert.NotEqual(t, a, b, "version bump should change rendered output")
	assert.Contains(t, string(a), "0.18.1")
	assert.Contains(t, string(b), "0.19.0")
	assert.NotContains(t, string(a), "0.19.0")
	assert.NotContains(t, string(b), "0.18.1")
}

// TestRenderInstallScript_FilestashAdminPasswordMatchesProjectName pins
// that the baked filestash config's auth.admin bcrypt verifies against
// the project name — so a user visiting https://files.<project>.<tld>/
// types their project name and nothing else.
func TestRenderInstallScript_FilestashAdminPasswordMatchesProjectName(t *testing.T) {
	const project = "my-cool-project"
	body, err := RenderInstallScript("0.18.1", project)
	require.NoError(t, err)

	// Extract the base64 config blob the template emitted between its
	// heredoc delimiters. The opening line carries a redirect after
	// the delimiter marker (`<<'FILESTASH_CFG_B64' > /home/devm/.../config.json`)
	// so match any trailing characters until the newline before the body.
	re := regexp.MustCompile(`(?s)<<'FILESTASH_CFG_B64'[^\n]*\n(.*?)\nFILESTASH_CFG_B64`)
	m := re.FindSubmatch(body)
	require.NotNil(t, m, "install.sh must contain the FILESTASH_CFG_B64 heredoc")

	cfgJSON, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(m[1])))
	require.NoError(t, err)

	var cfg map[string]any
	require.NoError(t, json.Unmarshal(cfgJSON, &cfg))

	auth, ok := cfg["auth"].(map[string]any)
	require.True(t, ok, "config has auth block")
	hash, ok := auth["admin"].(string)
	require.True(t, ok, "auth.admin is a string")

	require.NoError(t, bcrypt.CompareHashAndPassword([]byte(hash), []byte(project)),
		"bcrypt hash must verify against the project name")
	// And it must NOT verify the previous shared constant.
	require.Error(t, bcrypt.CompareHashAndPassword([]byte(hash), []byte("devm")),
		"bcrypt hash must not verify against the former shared password")
}

// TestRenderInstallScript_DifferentProjectsGetDifferentAdminHashes pins
// that two projects' bundles don't share an admin hash — otherwise the
// same password would unlock any guest.
func TestRenderInstallScript_DifferentProjectsGetDifferentAdminHashes(t *testing.T) {
	a, err := RenderInstallScript("0.18.1", "projA")
	require.NoError(t, err)
	b, err := RenderInstallScript("0.18.1", "projB")
	require.NoError(t, err)
	assert.NotEqual(t, a, b, "project name change should alter rendered install.sh")
}
