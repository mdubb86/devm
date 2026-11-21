package serviceapi

import "fmt"

// previewServePort is the guest-side port gdevm serve binds (see
// cmd/gdevm/serve.go). Shared with the /v1/health route; the Host
// header disambiguates on the guest side.
const previewServePort = 8940

// reservedPreviewRoute builds the daemon-owned route exposing the
// guest's gdevm-serve preview endpoint at preview.<project>.<tld>.
// Same shape as reservedFilestashRoute.
func reservedPreviewRoute(projectName, projectIP, tld string) Route {
	return Route{
		Hostname:    "preview." + projectName + "." + tld,
		BackendHost: projectIP,
		BackendPort: previewServePort,
		Mode:        ModeVM,
		Project:     projectName,
	}
}

// IsReservedPreviewHostname reports whether hostname is the reserved
// preview-route name for projectID + tld. Used by Routes.Apply to
// reject a devm.yaml declaring a service at the reserved name.
func IsReservedPreviewHostname(hostname, projectID, tld string) bool {
	return hostname == "preview."+projectID+"."+tld
}

// FormatReservedPreviewCollisionError is the user-facing error returned
// whenever a devm.yaml declares a service at the reserved
// preview.<project>.<tld> hostname. Both the daemon-side guard
// (Routes.Apply) and the CLI pre-flight share this phrasing.
func FormatReservedPreviewCollisionError(hostname string) error {
	return fmt.Errorf(
		"hostname %q is reserved for devm's bundled preview server — "+
			"remove this entry from devm.yaml; the preview surface is "+
			"auto-served at https://%s",
		hostname, hostname,
	)
}
