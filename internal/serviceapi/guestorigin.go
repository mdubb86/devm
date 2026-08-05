package serviceapi

import (
	"net"
	"strconv"
)

// guestOriginBackend resolves a Host header from guest-originated `.test`
// traffic to the address the guest-origin listener dials.
//
// The backend is always this project's own guest at projectIP:<service-port>,
// never the route's BackendHost. That pin is deliberate and load-bearing: a
// project in `devm route local` mode has Mac-local backends, and honoring them
// here would hand the in-guest agent reachability to services on the Mac that
// it otherwise has no path to. The guest-origin listener exists to let the
// guest reach its own services over TLS and nothing else.
//
// Direct services are excluded by Routes.Lookup — they stay raw TCP
// end-to-end; softnet's .test DNS answers loopback for them and their
// traffic never leaves the guest.
func guestOriginBackend(routes *Routes, host, projectID, projectIP string) (string, bool) {
	if projectIP == "" {
		return "", false
	}
	route, ok := routes.Lookup(stripPort(host), projectID)
	if !ok {
		return "", false
	}
	return net.JoinHostPort(projectIP, strconv.Itoa(route.BackendPort)), true
}
