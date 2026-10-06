package render

import (
	"bytes"
	"text/template"

	"github.com/mdubb86/devm/internal/scripts"
)

// RenderGdevmServeUnit returns the systemd unit file for gdevm-serve —
// devm's own guest-side state daemon (see cmd/gdevm/serve.go). Unlike
// schema.Service units (RenderService), this unit is devm-owned
// infrastructure, not user-declared: it's shipped unconditionally by
// devmbundle.Build, present in every bundle regardless of devm.yaml
// contents. The only templated value is DEVM_TLD, the daemon's identity
// TLD, which the in-guest handler uses to match the preview hostname.
func RenderGdevmServeUnit(tld string) []byte {
	t := template.Must(template.New("gdevm-serve.service").Parse(scripts.GdevmServeUnit))
	var buf bytes.Buffer
	if err := t.Execute(&buf, struct{ TLD string }{TLD: tld}); err != nil {
		panic("gdevm-serve.service template execution: " + err.Error())
	}
	return buf.Bytes()
}
