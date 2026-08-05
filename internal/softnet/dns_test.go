package softnet

import (
	"context"
	"net"
	"testing"
)

// TestPolicyResolverPerQuery exercises policyResolver.resolver(), the
// integration point where startDNS's dns.NewWithUpstreamResolver consults the
// LIVE egress policy on every query (not just once at server construction).
// It asserts the mapping across all three policies and, crucially, flips the
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

	// OPEN: host resolver.
	e.setPolicy(PolicyOpen, nil)
	res, err := r.resolver()
	if err != nil {
		t.Fatalf("OPEN: unexpected error: %v", err)
	}
	if res != net.DefaultResolver {
		t.Fatalf("OPEN: want net.DefaultResolver, got %v", res)
	}

	// ENFORCED with a configured DNS endpoint: custom resolver dialing
	// iron-proxy's DNS address, not the host resolver.
	e.setPolicy(PolicyEnforced, &ForwardTargets{DNS: "127.0.0.1:5353"})
	res, err = r.resolver()
	if err != nil {
		t.Fatalf("ENFORCED: unexpected error: %v", err)
	}
	if res == net.DefaultResolver {
		t.Fatal("ENFORCED: must not use net.DefaultResolver")
	}
	if res.Dial == nil {
		t.Fatal("ENFORCED: want a custom Dial pointed at iron-proxy's DNS")
	}

	// ENFORCED with no DNS endpoint configured: drop.
	e.setPolicy(PolicyEnforced, &ForwardTargets{})
	if res, err := r.resolver(); err == nil {
		t.Fatalf("ENFORCED (empty endpoint): want error (drop), got resolver %v", res)
	}

	// Flip back to OPEN on the SAME resolver instance to prove resolver()
	// re-derives the upstream per call from e's live policy, rather than
	// having captured a decision once.
	e.setPolicy(PolicyOpen, nil)
	res, err = r.resolver()
	if err != nil {
		t.Fatalf("OPEN (after flip back): unexpected error: %v", err)
	}
	if res != net.DefaultResolver {
		t.Fatalf("OPEN (after flip back): want net.DefaultResolver, got %v", res)
	}
}

func TestUpstreamFor(t *testing.T) {
	ft := &ForwardTargets{DNS: "127.0.0.1:8053"}

	if _, _, ok := upstreamFor(PolicyLocked, ft); ok {
		t.Fatal("LOCKED: no DNS upstream")
	}
	if _, useHost, ok := upstreamFor(PolicyOpen, ft); !ok || !useHost {
		t.Fatal("OPEN: must use the host resolver")
	}
	addr, useHost, ok := upstreamFor(PolicyEnforced, ft)
	if !ok || useHost || addr != ft.DNS {
		t.Fatalf("ENFORCED: want iron-proxy DNS %s, got %q useHost=%v ok=%v", ft.DNS, addr, useHost, ok)
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

	for _, pol := range []Policy{PolicyLocked, PolicyOpen, PolicyEnforced} {
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
