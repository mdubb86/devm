package serviceapi

import "fmt"

// filestashServePort is the static port every devm guest's bundled
// filestash service listens on. Referenced by computeExposeMap (Mac
// softnet forward), reservedFilestashRoute (Mac-side reverse-proxy
// backend), and the guest-side systemd unit's ExecStart flags — all
// three MUST agree, and this constant is the single source.
const filestashServePort = 8941

// reservedFilestashRoute builds the daemon-owned route that exposes
// the guest's filestash service at a stable Mac-side hostname. Same
// shape as reservedHealthRoute: the daemon installs it directly at
// /vm/start (applyReservedRoute), the ProxyServer serves it to any
// browser reaching https://files.<project>.<tld>, TLS terminates with
// devm's local CA. Recovered on daemon restart by recoverProjectState.
//
// tld is identity.Config.TLD ("test" for prod, "e2e.test" for the e2e
// slot).
func reservedFilestashRoute(projectName, projectIP, tld string) Route {
	return Route{
		Hostname:    "files." + projectName + "." + tld,
		BackendHost: projectIP,
		BackendPort: filestashServePort,
		Mode:        ModeVM,
		Project:     projectName,
	}
}

// IsReservedFilesHostname reports whether hostname is the reserved
// filestash-route name for projectID + tld. Used by Routes.Apply to
// reject a user's devm.yaml declaring a service at the reserved name
// — the filestash bundle owns it exclusively.
func IsReservedFilesHostname(hostname, projectID, tld string) bool {
	return hostname == "files."+projectID+"."+tld
}

// FormatReservedFilesCollisionError builds the user-facing error returned
// whenever a devm.yaml declares a service at the reserved
// files.<project>.<tld> hostname. The two sites that enforce the rule —
// Routes.Apply (daemon-side route-registration guard) and
// cmd/devm/shell.go's rejectReservedFilesHostname (CLI-side pre-flight,
// hit by `devm validate` and `devm start`/`shell`/`reconcile`) — share
// this one phrasing so the error reads identically regardless of which
// gate caught it.
func FormatReservedFilesCollisionError(hostname string) error {
	return fmt.Errorf(
		"hostname %q is reserved for devm's bundled filestash service — "+
			"remove this entry from devm.yaml; the file browser is auto-served at https://%s",
		hostname, hostname,
	)
}
