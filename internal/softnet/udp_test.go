package softnet

import "testing"

func TestUDPTarget(t *testing.T) {
	ft := &ForwardTargets{NTP: "127.0.0.1:8123"}
	e := newEgress(nil)

	e.setPolicy(PolicyLocked, ft)
	if _, ok := e.udpTarget("1.2.3.4", 123); ok {
		t.Fatal("LOCKED must deny udp")
	}

	e.setPolicy(PolicyForwarding, ft)
	if got, ok := e.udpTarget("1.2.3.4", 123); !ok || got != ft.NTP {
		t.Fatalf("FORWARDING udp:123 = %q,%v want %s", got, ok, ft.NTP)
	}
	if _, ok := e.udpTarget("1.2.3.4", 53); ok {
		t.Fatal("FORWARDING must deny non-123 udp (DNS is a bound endpoint, not here)")
	}
	e.setPolicy(PolicyForwarding, &ForwardTargets{})
	if _, ok := e.udpTarget("1.2.3.4", 123); ok {
		t.Fatal("FORWARDING with empty NTP must deny")
	}
}
