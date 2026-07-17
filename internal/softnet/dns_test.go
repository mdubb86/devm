package softnet

import (
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
	e.setPolicy(PolicyEnforced, &IronProxyEndpoint{DNS: "127.0.0.1:5353"})
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
	e.setPolicy(PolicyEnforced, &IronProxyEndpoint{})
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
	ip := &IronProxyEndpoint{DNS: "127.0.0.1:8053"}

	if _, _, ok := upstreamFor(PolicyLocked, ip); ok {
		t.Fatal("LOCKED: no DNS upstream")
	}
	if _, useHost, ok := upstreamFor(PolicyOpen, ip); !ok || !useHost {
		t.Fatal("OPEN: must use the host resolver")
	}
	addr, useHost, ok := upstreamFor(PolicyEnforced, ip)
	if !ok || useHost || addr != ip.DNS {
		t.Fatalf("ENFORCED: want iron-proxy DNS %s, got %q useHost=%v ok=%v", ip.DNS, addr, useHost, ok)
	}
}
