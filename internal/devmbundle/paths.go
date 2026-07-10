// Package devmbundle builds the tar archive of devm-owned artifacts
// that the daemon pipes into the guest during cold-start and reconcile.
// See docs/superpowers/specs/2026-07-10-devm-bundle-design.md.
package devmbundle

// Guest-side paths after the bundle is extracted. Every caller that
// needs to reference a bundle artifact from Go code uses one of these
// constants; nothing else knows the layout.
const (
	GuestRoot         = "/opt/devm"
	GuestEnv          = GuestRoot + "/.env"
	GuestWrapper      = GuestRoot + "/scripts/with-devm-env"
	GuestDispatcher   = GuestRoot + "/scripts/install-templates.sh"
	GuestTemplatesDir = GuestRoot + "/templates"

	// GuestInstallScript is the shell command the daemon runs inside
	// the guest with the tar bytes on stdin. Extracts the bundle to
	// /opt/devm and runs the bundle's install.sh.
	GuestInstallScript = "sudo mkdir -p /opt/devm && sudo tar -xC /opt/devm -f - && sudo /opt/devm/install.sh"
)
