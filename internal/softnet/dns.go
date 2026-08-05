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

// dnsZoneName is the zone startDNS answers locally regardless of policy,
// resolving every <name>.test to NATAliasIP (the host loopback NAT alias).
const dnsZoneName = "devm.test."

// upstreamFor picks the DNS upstream for the current policy. useHost=true means
// resolve via the Mac's own resolver (net.DefaultResolver) for direct
// provisioning; otherwise dial addr (iron-proxy's DNS). ok=false => drop.
func upstreamFor(pol Policy, ft *ForwardTargets) (addr string, useHost bool, ok bool) {
	switch pol {
	case PolicyOpen:
		return "", true, true
	case PolicyEnforced:
		if ft == nil || ft.DNS == "" {
			return "", false, false
		}
		return ft.DNS, false, true
	default:
		return "", false, false
	}
}

// policyResolver implements the gvisor-tap-vsock dns package's unexported
// upstream-resolver interface (LookupIPAddr/LookupCNAME/LookupMX/LookupNS/
// LookupSRV/LookupTXT). Every lookup re-derives the upstream from e's live
// policy via upstreamFor, so a policy change takes effect on the next query
// with no server restart.
type policyResolver struct{ e *egress }

// resolver picks the *net.Resolver to use for one lookup: the host resolver
// in OPEN, or one dialing iron-proxy's DNS address in ENFORCED. Returns an
// error when the policy says to drop (LOCKED, or ENFORCED with no DNS
// configured), which the dns package surfaces as a lookup failure (NXDOMAIN).
func (r *policyResolver) resolver() (*net.Resolver, error) {
	pol, ft := r.e.snapshot()
	addr, useHost, ok := upstreamFor(pol, ft)
	if !ok {
		return nil, fmt.Errorf("softnet: dns upstream dropped by policy %s", pol)
	}
	if useHost {
		return net.DefaultResolver, nil
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

// startDNS binds gateway:53 (UDP and TCP) and serves DNS for the guest. The
// devm.test zone is answered locally (-> NATAliasIP) regardless of policy;
// every other name resolves through a policyResolver that re-consults e's
// live policy per query. Ported from the fixture's startDNS
// (e2e/contract/softnet/main.go): newNetwork's types.Configuration carries no
// DNS zones (dropped in Task 3, nothing consumed them then), so the zone is
// built here instead of read off n's config.
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

	zones := []types.Zone{{
		Name:      dnsZoneName,
		DefaultIP: net.ParseIP(NATAliasIP),
	}}
	server, err := dns.NewWithUpstreamResolver(udpConn, tcpLn, zones, &policyResolver{e: e})
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
