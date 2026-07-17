package softnet

import "testing"

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
