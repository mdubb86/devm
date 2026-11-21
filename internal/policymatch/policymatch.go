// Package policymatch decides whether a request is covered by a
// network.allow list. It is the daemon-side authority behind iron-proxy's
// grpc transform (see docs/superpowers/specs/2026-08-29-grpc-policy-
// authority-design.md) and MUST mirror iron-proxy's own allowlist
// matching semantics (internal/hostmatch in the upstream tree) exactly:
// this is the single place the decision is made, and a request the
// daemon allows is one iron-proxy would have allowed under the built-in
// allowlist transform, and vice versa.
//
// Semantics:
//   - "*" matches any host.
//   - "*.example.com" matches any subdomain depth AND "example.com" itself.
//   - Any other host pattern uses path.Match glob semantics ("*" does not
//     cross dots).
//   - An entry may carry a path pattern after the host
//     ("host/subtree/*"): the host part matches as above, and the request
//     path must match the pattern. Patterns ending "/*" match the whole
//     subtree including the base ("/v1/*" matches "/v1"); anything else
//     is a segment-wise path.Match glob.
//   - Entries OR together; a host-only entry allows every path on that
//     host regardless of path-scoped entries for the same host.
package policymatch

import (
	"net"
	"path"
	"strings"

	"github.com/mdubb86/devm/internal/schema"
)

// Allowed reports whether a request to host (no port) with the given URL
// path is covered by the allow list. Entries are network.allow strings,
// optionally path-scoped ("host/pattern").
func Allowed(allow []string, host, reqPath string) bool {
	for _, raw := range allow {
		e := schema.AllowEntry{Host: raw}
		if !matchGlob(e.HostPart(), host) {
			continue
		}
		p := e.PathPattern()
		if p == "" || matchPath(p, reqPath) {
			return true
		}
	}
	return false
}

// matchGlob mirrors iron-proxy's hostmatch.MatchGlob.
func matchGlob(pattern, name string) bool {
	if pattern == "*" {
		return true
	}
	if strings.HasPrefix(pattern, "*.") {
		suffix := pattern[1:] // ".example.com"
		return strings.HasSuffix(name, suffix) || name == pattern[2:]
	}
	matched, _ := path.Match(pattern, name)
	return matched
}

// matchPath mirrors iron-proxy's hostmatch.MatchPath.
func matchPath(pattern, reqPath string) bool {
	if strings.HasSuffix(pattern, "/*") {
		prefix := pattern[:len(pattern)-1] // "/v1/"
		base := pattern[:len(pattern)-2]   // "/v1"
		return strings.HasPrefix(reqPath, prefix) || reqPath == base
	}
	matched, _ := path.Match(pattern, reqPath)
	return matched
}

// StripPort removes the port from a host:port string; a bare host is
// returned unchanged. Mirrors iron-proxy's hostmatch.StripPort.
func StripPort(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}
