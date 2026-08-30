package render

import (
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

func TestRenderInstallScript_RejectsEmptyVersion(t *testing.T) {
	_, err := RenderInstallScript("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutagen version")
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
