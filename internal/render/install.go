package render

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"text/template"

	"github.com/mdubb86/devm/internal/scripts"
)

// RenderInstallScript substitutes {{.MutagenVersion}} and the bundled
// filestash blobs in scripts.InstallTemplate and returns the rendered
// install.sh body. Used by devmbundle at bundle-build time so the
// guest's install.sh points at the mutagen-agent path scoped by the
// mutagen version we pin, and carries the filestash binary, config,
// and systemd unit inline.
func RenderInstallScript(mutagenVersion string) ([]byte, error) {
	if mutagenVersion == "" {
		return nil, fmt.Errorf("render install script: mutagen version is required")
	}
	t, err := template.New("install.sh").Parse(scripts.InstallTemplate)
	if err != nil {
		return nil, fmt.Errorf("render install script: parse template: %w", err)
	}
	data := struct {
		MutagenVersion     string
		FilestashBinaryB64 string
		FilestashConfigB64 string
		FilestashUnit      string
	}{
		MutagenVersion:     mutagenVersion,
		FilestashBinaryB64: base64.StdEncoding.EncodeToString(scripts.EmbeddedFilestashBinary),
		FilestashConfigB64: base64.StdEncoding.EncodeToString(scripts.EmbeddedFilestashConfig),
		FilestashUnit:      scripts.EmbeddedFilestashSystemdUnit,
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("render install script: execute template: %w", err)
	}
	return buf.Bytes(), nil
}
