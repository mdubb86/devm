package softnet

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/containers/gvisor-tap-vsock/pkg/services/dns"
	"github.com/containers/gvisor-tap-vsock/pkg/types"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
)

// dnsZoneName and hostTestZoneName are the two zones startDNS answers
// locally regardless of policy; every <name>.devm.test and
// <name>.host.test resolves to NATAliasIP, the host loopback NAT alias.
// host.test. is the reserved zone for mac_services — gvproxy NATs
// NATAliasIP to 127.0.0.1 on the Mac, and the port allowlist pushed via
// setPolicy decides whether the packet reaches a Mac service or hits the
// forwarder's RST.
const (
	dnsZoneName      = "devm.test."
	hostTestZoneName = "host.test."
)

// dnsZones returns the local DNS zones startDNS answers regardless of
// policy. Kept as a function so tests can bind the same slice to a
// loopback DNS server and prove the mapping without standing up the full
// gvisor stack.
func dnsZones() []types.Zone {
	return []types.Zone{
		{Name: dnsZoneName, DefaultIP: net.ParseIP(NATAliasIP)},
		{Name: hostTestZoneName, DefaultIP: net.ParseIP(NATAliasIP)},
	}
}

// upstreamFor picks the DNS upstream for the current policy. LOCKED drops;
// FORWARDING dials addr (iron-proxy's DNS). ok=false => drop.
func upstreamFor(pol Policy, ft *ForwardTargets) (addr string, ok bool) {
	switch pol {
	case PolicyForwarding:
		if ft == nil || ft.DNS == "" {
			return "", false
		}
		return ft.DNS, true
	default:
		return "", false
	}
}

// policyResolver implements the gvisor-tap-vsock dns package's unexported
// upstream-resolver interface (LookupIPAddr/LookupCNAME/LookupMX/LookupNS/
// LookupSRV/LookupTXT). Every lookup re-derives the upstream from e's live
// policy via upstreamFor, so a policy change takes effect on the next query
// with no server restart.
type policyResolver struct{ e *egress }

// resolver picks the *net.Resolver to use for one lookup: LOCKED drops
// (returns an error, which the dns package surfaces as a lookup failure —
// NXDOMAIN); FORWARDING dials iron-proxy's DNS address. Returns an error
// when the policy says to drop (LOCKED, or FORWARDING with no DNS
// configured).
func (r *policyResolver) resolver() (*net.Resolver, error) {
	pol, ft := r.e.snapshot()
	addr, ok := upstreamFor(pol, ft)
	if !ok {
		return nil, fmt.Errorf("softnet: dns upstream dropped by policy %s", pol)
	}
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}, nil
}

func (r *policyResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	if ip, ok := r.e.testAnswer(host); ok {
		return []net.IPAddr{{IP: ip}}, nil
	}
	res, err := r.resolver()
	if err != nil {
		return nil, err
	}
	return res.LookupIPAddr(ctx, host)
}

// testAnswerIP is the .test answer table. fqdn arrives in miekg form with a
// trailing dot. Direct hostnames answer HostLoopIP; every other name in (or
// equal to) the test TLD answers InterceptedTestIP; anything else is not
// intercepted (ok=false) and falls through to the policy upstream. Names
// matching a vendored DNS zone (e.g. *.devm.test) never reach this function
// — zone matching runs first in the handler.
func testAnswerIP(fqdn string, direct map[string]struct{}) (net.IP, bool) {
	name := strings.TrimSuffix(fqdn, ".")
	if name != "test" && !strings.HasSuffix(name, ".test") {
		return nil, false
	}
	if _, ok := direct[name]; ok {
		return net.ParseIP(HostLoopIP), true
	}
	return net.ParseIP(InterceptedTestIP), true
}

func (r *policyResolver) LookupCNAME(ctx context.Context, host string) (string, error) {
	res, err := r.resolver()
	if err != nil {
		return "", err
	}
	return res.LookupCNAME(ctx, host)
}

func (r *policyResolver) LookupMX(ctx context.Context, name string) ([]*net.MX, error) {
	res, err := r.resolver()
	if err != nil {
		return nil, err
	}
	return res.LookupMX(ctx, name)
}

func (r *policyResolver) LookupNS(ctx context.Context, name string) ([]*net.NS, error) {
	res, err := r.resolver()
	if err != nil {
		return nil, err
	}
	return res.LookupNS(ctx, name)
}

func (r *policyResolver) LookupSRV(ctx context.Context, service, proto, name string) (string, []*net.SRV, error) {
	res, err := r.resolver()
	if err != nil {
		return "", nil, err
	}
	return res.LookupSRV(ctx, service, proto, name)
}

func (r *policyResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	res, err := r.resolver()
	if err != nil {
		return nil, err
	}
	return res.LookupTXT(ctx, name)
}

// startDNS binds gateway:53 (UDP and TCP) and serves DNS for the guest.
// Names under dnsZones() are answered locally (-> NATAliasIP) regardless
// of policy; every other name resolves through a policyResolver that
// re-consults e's live policy per query.
func (n *network) startDNS(e *egress) error {
	gatewayAddr := tcpip.AddrFrom4Slice(net.ParseIP(GatewayIP).To4())

	udpConn, err := gonet.DialUDP(n.stack, &tcpip.FullAddress{
		NIC: 1, Addr: gatewayAddr, Port: 53,
	}, nil, ipv4.ProtocolNumber)
	if err != nil {
		return err
	}
	tcpLn, err := gonet.ListenTCP(n.stack, tcpip.FullAddress{
		NIC: 1, Addr: gatewayAddr, Port: 53,
	}, ipv4.ProtocolNumber)
	if err != nil {
		return err
	}

	server, err := dns.NewWithUpstreamResolver(udpConn, tcpLn, dnsZones(), &policyResolver{e: e})
	if err != nil {
		return err
	}
	go func() {
		if err := server.Serve(); err != nil {
			logf("dns serve: %v", err)
		}
	}()
	go func() {
		if err := server.ServeTCP(); err != nil {
			logf("dns serve tcp: %v", err)
		}
	}()
	return nil
}
