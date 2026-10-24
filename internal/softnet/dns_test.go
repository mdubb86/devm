package softnet

import (
	"context"
	"net"
	"testing"
	"time"

	dnssvc "github.com/containers/gvisor-tap-vsock/pkg/services/dns"
	mdns "github.com/miekg/dns"
)

// TestPolicyResolverPerQuery exercises policyResolver.resolver(), the
// integration point where startDNS's dns.NewWithUpstreamResolver consults the
// LIVE egress policy on every query (not just once at server construction).
// It asserts the mapping across both policies and, crucially, flips the
// policy between calls on the SAME resolver to prove each call re-derives
// its upstream from e rather than capturing one at construction time.
func TestPolicyResolverPerQuery(t *testing.T) {
	e := newEgress(nil)
	r := &policyResolver{e: e}

	// LOCKED: no usable resolver, drop.
	e.setPolicy(PolicyLocked, nil)
	if res, err := r.resolver(); err == nil {
		t.Fatalf("LOCKED: want error (drop), got resolver %v", res)
	}

	// FORWARDING with a configured DNS endpoint: custom resolver dialing
	// iron-proxy's DNS address, not the host resolver.
	e.setPolicy(PolicyForwarding, &ForwardTargets{DNS: "127.0.0.1:5353"})
	res, err := r.resolver()
	if err != nil {
		t.Fatalf("FORWARDING: unexpected error: %v", err)
	}
	if res == net.DefaultResolver {
		t.Fatal("FORWARDING: must not use net.DefaultResolver")
	}
	if res.Dial == nil {
		t.Fatal("FORWARDING: want a custom Dial pointed at iron-proxy's DNS")
	}

	// FORWARDING with no DNS endpoint configured: drop.
	e.setPolicy(PolicyForwarding, &ForwardTargets{})
	if res, err := r.resolver(); err == nil {
		t.Fatalf("FORWARDING (empty endpoint): want error (drop), got resolver %v", res)
	}

	// Flip back to a configured FORWARDING endpoint on the SAME resolver
	// instance to prove resolver() re-derives the upstream per call from
	// e's live policy, rather than having captured a decision once.
	e.setPolicy(PolicyForwarding, &ForwardTargets{DNS: "127.0.0.1:5353"})
	res, err = r.resolver()
	if err != nil {
		t.Fatalf("FORWARDING (after flip back): unexpected error: %v", err)
	}
	if res == net.DefaultResolver {
		t.Fatal("FORWARDING (after flip back): must not use net.DefaultResolver")
	}
	if res.Dial == nil {
		t.Fatal("FORWARDING (after flip back): want a custom Dial pointed at iron-proxy's DNS")
	}
}

func TestUpstreamFor(t *testing.T) {
	ft := &ForwardTargets{DNS: "127.0.0.1:8053"}

	if _, ok := upstreamFor(PolicyLocked, ft); ok {
		t.Fatal("LOCKED: no DNS upstream")
	}
	addr, ok := upstreamFor(PolicyForwarding, ft)
	if !ok || addr != ft.DNS {
		t.Fatalf("FORWARDING: want iron-proxy DNS %s, got %q ok=%v", ft.DNS, addr, ok)
	}
}

// TestTestAnswerIP pins the .test DNS answer table. Names arrive in miekg
// FQDN form (trailing dot). Direct hostnames answer loopback so raw TCP
// stays in-guest; every other .test name answers the hairpin address; non
// .test names are not intercepted and fall through to the policy upstream.
func TestTestAnswerIP(t *testing.T) {
	direct := map[string]struct{}{"db.test": {}}

	cases := []struct {
		fqdn string
		want string // "" => not intercepted
	}{
		{"api.test.", InterceptedTestIP},
		{"foo.bar.test.", InterceptedTestIP},
		{"test.", InterceptedTestIP},
		{"db.test.", HostLoopIP},
		{"pretest.", ""},
		{"example.com.", ""},
		{"test.example.com.", ""},
	}
	for _, c := range cases {
		ip, ok := testAnswerIP(c.fqdn, direct)
		if c.want == "" {
			if ok {
				t.Fatalf("%s must not be intercepted, got %v", c.fqdn, ip)
			}
			continue
		}
		if !ok || ip.String() != c.want {
			t.Fatalf("%s = %v,%v want %s,true", c.fqdn, ip, ok, c.want)
		}
	}
}

// TestPolicyResolverAnswersTestUnderAnyPolicy pins that .test resolves even
// under LOCKED — DNS answers, then the TCP flow is RST by policy. Matches
// the previous behavior where guest dnsmasq answered locally regardless.
func TestPolicyResolverAnswersTestUnderAnyPolicy(t *testing.T) {
	e := newEgress(nil)
	e.setDirectTestHosts([]string{"db.test"})
	r := &policyResolver{e: e}

	for _, pol := range []Policy{PolicyLocked, PolicyForwarding} {
		e.setPolicy(pol, nil)
		addrs, err := r.LookupIPAddr(context.Background(), "api.test.")
		if err != nil || len(addrs) != 1 || addrs[0].IP.String() != InterceptedTestIP {
			t.Fatalf("%s api.test = %v,%v want [%s]", pol, addrs, err, InterceptedTestIP)
		}
		addrs, err = r.LookupIPAddr(context.Background(), "db.test.")
		if err != nil || len(addrs) != 1 || addrs[0].IP.String() != HostLoopIP {
			t.Fatalf("%s db.test = %v,%v want [%s]", pol, addrs, err, HostLoopIP)
		}
	}
}

// TestStartDNS_HostTestZone pins that every <anything>.host.test resolves
// locally to NATAliasIP, so a guest TCP flow to a Mac service name routes
// to the loopback NAT alias rather than leaking out to the policy
// upstream. Builds the real DNS server off the same dnsZones() startDNS
// uses, bound to a loopback UDP socket so the gvisor stack is not needed
// to exercise the name path.
func TestStartDNS_HostTestZone(t *testing.T) {
	udpConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	defer udpConn.Close()
	tcpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	defer tcpLn.Close()

	e := newEgress(nil)
	e.setPolicy(PolicyForwarding, &ForwardTargets{DNS: "127.0.0.1:0"})
	server, err := dnssvc.NewWithUpstreamResolver(udpConn, tcpLn, dnsZones(), &policyResolver{e: e})
	if err != nil {
		t.Fatalf("dns server: %v", err)
	}
	go func() { _ = server.Serve() }()

	for _, host := range []string{"api.host.test.", "foo.bar.host.test."} {
		if got := dnsLookupA(t, udpConn.LocalAddr().String(), host); got.String() != NATAliasIP {
			t.Fatalf("%s: want %s, got %s", host, NATAliasIP, got)
		}
	}
}

// dnsLookupA sends a type-A query for host to the UDP DNS server at
// serverAddr and returns the first A answer, failing the test with
// context on any transport or decode error.
func dnsLookupA(t *testing.T, serverAddr, host string) net.IP {
	t.Helper()
	c := &mdns.Client{Net: "udp", Timeout: 2 * time.Second}
	m := new(mdns.Msg)
	m.SetQuestion(host, mdns.TypeA)
	resp, _, err := c.Exchange(m, serverAddr)
	if err != nil {
		t.Fatalf("%s: exchange: %v", host, err)
	}
	if len(resp.Answer) == 0 {
		t.Fatalf("%s: no answers (rcode=%d)", host, resp.Rcode)
	}
	a, ok := resp.Answer[0].(*mdns.A)
	if !ok {
		t.Fatalf("%s: want *dns.A, got %T", host, resp.Answer[0])
	}
	return a.A
}

// TestSetDirectTestHostsReplaces pins replace (not merge) semantics so a
// removed direct service stops answering loopback on the next push.
func TestSetDirectTestHostsReplaces(t *testing.T) {
	e := newEgress(nil)
	e.setDirectTestHosts([]string{"db.test"})
	e.setDirectTestHosts([]string{"cache.test"})
	if ip, _ := e.testAnswer("db.test."); ip.String() != InterceptedTestIP {
		t.Fatalf("db.test after removal = %v want %s", ip, InterceptedTestIP)
	}
	if ip, _ := e.testAnswer("cache.test."); ip.String() != HostLoopIP {
		t.Fatalf("cache.test = %v want %s", ip, HostLoopIP)
	}
}
