// internal/softnet/config.go
package softnet

import "fmt"

// Network constants — match gvproxy defaults and the contract fixture.
const (
	SubnetCIDR = "192.168.127.0/24"
	GatewayIP  = "192.168.127.1"
	GatewayMAC = "5a:94:ef:e4:0c:dd"
	NATAliasIP = "192.168.127.254" // NATs to the host's 127.0.0.1
	HostLoopIP = "127.0.0.1"
	MTU        = 1500
	// GuestLeaseIP is the deterministic first DHCP lease handed to the sole
	// guest (.1 is the gateway). Ingress dials this; egress does not need it.
	GuestLeaseIP = "192.168.127.2"

	// InterceptedTestIP is the hairpin address softnet's own DNS answers for
	// every non-direct `*.test` name. The egress forwarder matches it by
	// destination IP and dials the daemon's guest-origin listener, which
	// terminates TLS with the devm CA and dials back into the guest. RFC 5737
	// documentation space, so it can never collide with a real destination.
	// Never appears in guest config — the guest learns it only via DNS.
	InterceptedTestIP = "192.0.2.2"
)

// Policy is softnet's coarse egress state, set by the daemon.
type Policy int

const (
	PolicyLocked   Policy = iota // drop all egress (boot lock)
	PolicyOpen                   // forward anywhere, direct (provisioning)
	PolicyEnforced               // forward :80/:443/:53/:123 to iron-proxy/NTP
)

func (p Policy) String() string {
	switch p {
	case PolicyLocked:
		return "LOCKED"
	case PolicyOpen:
		return "OPEN"
	case PolicyEnforced:
		return "ENFORCED"
	default:
		return fmt.Sprintf("Policy(%d)", int(p))
	}
}

func ParsePolicy(s string) (Policy, error) {
	switch s {
	case "LOCKED":
		return PolicyLocked, nil
	case "OPEN":
		return PolicyOpen, nil
	case "ENFORCED":
		return PolicyEnforced, nil
	default:
		return 0, fmt.Errorf("unknown policy %q", s)
	}
}

// ForwardTargets is where softnet forwards guest-originated TCP. Each field is
// a host:port on the Mac. HTTP/HTTPS/DNS/NTP are the egress path (iron-proxy
// and devm's SNTP responder); GuestHTTP/GuestHTTPS are the daemon's
// guest-origin listener, which loops `.test` traffic back into this project's
// own services.
type ForwardTargets struct {
	HTTP  string `json:"http"`
	HTTPS string `json:"https"`
	DNS   string `json:"dns"`
	NTP   string `json:"ntp"`

	GuestHTTP  string `json:"guest_http,omitempty"`
	GuestHTTPS string `json:"guest_https,omitempty"`
}

// ExposePort is one host->guest ingress mapping.
type ExposePort struct {
	GuestPort int    `json:"guest_port"`
	BindIP    string `json:"bind_ip"`
	HostPort  int    `json:"host_port"`
}

// ControlMsg is one line of the daemon->softnet control protocol (JSON per
// line over the unix socket). Op is "setPolicy", "setExposeMap", or
// "setTestHosts".
type ControlMsg struct {
	Op              string          `json:"op"`
	Policy          string          `json:"policy,omitempty"`
	ForwardTargets  *ForwardTargets `json:"forward_targets,omitempty"`
	Expose          []ExposePort    `json:"expose,omitempty"`
	DirectTestHosts []string        `json:"direct_test_hosts,omitempty"`
}
