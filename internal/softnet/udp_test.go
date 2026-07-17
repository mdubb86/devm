package softnet

import "testing"

func TestUDPTarget(t *testing.T) {
	ep := &IronProxyEndpoint{NTP: "127.0.0.1:8123"}
	e := newEgress(nil)

	e.setPolicy(PolicyLocked, ep)
	if _, ok := e.udpTarget("1.2.3.4", 123); ok {
		t.Fatal("LOCKED must deny udp")
	}

	e.setPolicy(PolicyOpen, ep)
	if got, ok := e.udpTarget("1.2.3.4", 123); !ok || got != "1.2.3.4:123" {
		t.Fatalf("OPEN udp = %q,%v want 1.2.3.4:123,true", got, ok)
	}
	if got, ok := e.udpTarget(NATAliasIP, 123); !ok || got != HostLoopIP+":123" {
		t.Fatalf("OPEN NAT-alias udp = %q,%v want %s:123", got, ok, HostLoopIP)
	}

	e.setPolicy(PolicyEnforced, ep)
	if got, ok := e.udpTarget("1.2.3.4", 123); !ok || got != ep.NTP {
		t.Fatalf("ENFORCED udp:123 = %q,%v want %s", got, ok, ep.NTP)
	}
	if _, ok := e.udpTarget("1.2.3.4", 53); ok {
		t.Fatal("ENFORCED must deny non-123 udp (DNS is a bound endpoint, not here)")
	}
	e.setPolicy(PolicyEnforced, &IronProxyEndpoint{})
	if _, ok := e.udpTarget("1.2.3.4", 123); ok {
		t.Fatal("ENFORCED with empty NTP must deny")
	}
}
