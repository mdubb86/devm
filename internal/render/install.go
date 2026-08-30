package render

import (
	"bytes"
	"fmt"
	"text/template"

	"github.com/mdubb86/devm/internal/scripts"
)

// RenderInstallScript substitutes {{.MutagenVersion}} in scripts.
// InstallTemplate and returns the rendered install.sh body. Used by
// devmbundle at bundle-build time so the guest's install.sh points at
// the mutagen-agent path scoped by the mutagen version we pin.
func RenderInstallScript(mutagenVersion string) ([]byte, error) {
	if mutagenVersion == "" {
		return nil, fmt.Errorf("render install script: mutagen version is required")
	}
	t, err := template.New("install.sh").Parse(scripts.InstallTemplate)
	if err != nil {
		return nil, fmt.Errorf("render install script: parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, struct{ MutagenVersion string }{MutagenVersion: mutagenVersion}); err != nil {
		return nil, fmt.Errorf("render install script: execute template: %w", err)
	}
	return buf.Bytes(), nil
}
