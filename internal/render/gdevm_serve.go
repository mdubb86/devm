package render

import "github.com/mdubb86/devm/internal/scripts"

// RenderGdevmServeUnit returns the systemd unit file for gdevm-serve —
// devm's own guest-side state daemon (see cmd/gdevm/serve.go). Unlike
// schema.Service units (RenderService), this unit is devm-owned
// infrastructure, not user-declared: it's shipped unconditionally by
// devmbundle.Build, present in every bundle regardless of devm.yaml
// contents, and static — no per-project templating.
func RenderGdevmServeUnit() []byte {
	return []byte(scripts.GdevmServeUnit)
}
