package render

import (
	"sort"
	"strings"

	"github.com/mdubb86/devm/internal/schema"
)

// HostsFragment generates the body of .devm/hosts.fragment: one
// `127.0.0.1 <hostname>` line per service that declares a non-empty
// hostname. Sorted by hostname for determinism.
//
// devm-startup.sh (startup step 1) applies this fragment to /etc/hosts
// inside the sandbox at cold-start / restart, delimited by BEGIN/END
// markers so we don't trample unrelated entries.
//
// Always returns a value, possibly empty. When empty, devm-startup.sh
// still strips any pre-existing devm block from /etc/hosts (the user
// may have just removed all `hostname:` fields from devm.yaml).
//
// All entries point at 127.0.0.1: in-VM caddy listens on the loopback
// interface (port 80), and the Caddyfile reverse_proxies each
// hostname to the actual service's in-VM listen port.
func HostsFragment(cfg schema.Config) string {
	var hostnames []string
	for _, svc := range cfg.Services {
		if svc.Hostname != "" {
			hostnames = append(hostnames, svc.Hostname)
		}
	}
	sort.Strings(hostnames)

	var sb strings.Builder
	for _, h := range hostnames {
		sb.WriteString("127.0.0.1 ")
		sb.WriteString(h)
		sb.WriteByte('\n')
	}
	return sb.String()
}
