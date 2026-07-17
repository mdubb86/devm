package softnet

import "testing"

func TestEgressTarget(t *testing.T) {
	ep := &IronProxyEndpoint{HTTP: "127.0.0.1:8080", HTTPS: "127.0.0.1:8443", DNS: "127.0.0.1:8053", NTP: "127.0.0.1:8123"}
	e := newEgress(nil)

	// LOCKED: everything denied.
	e.setPolicy(PolicyLocked, ep)
	if _, ok := e.target("1.2.3.4", 443); ok {
		t.Fatal("LOCKED must deny :443")
	}

	// OPEN: forward direct to the original dst:port.
	e.setPolicy(PolicyOpen, ep)
	if got, ok := e.target("1.2.3.4", 443); !ok || got != "1.2.3.4:443" {
		t.Fatalf("OPEN :443 = %q,%v want 1.2.3.4:443,true", got, ok)
	}
	if got, ok := e.target("9.9.9.9", 12345); !ok || got != "9.9.9.9:12345" {
		t.Fatalf("OPEN arbitrary port must pass direct, got %q,%v", got, ok)
	}

	// ENFORCED: :80/:443 -> iron-proxy; other ports denied.
	e.setPolicy(PolicyEnforced, ep)
	if got, ok := e.target("192.0.2.1", 443); !ok || got != ep.HTTPS {
		t.Fatalf("ENFORCED :443 = %q,%v want %s", got, ok, ep.HTTPS)
	}
	if got, ok := e.target("192.0.2.1", 80); !ok || got != ep.HTTP {
		t.Fatalf("ENFORCED :80 = %q,%v want %s", got, ok, ep.HTTP)
	}
	if _, ok := e.target("192.0.2.1", 5432); ok {
		t.Fatal("ENFORCED must deny non-80/443 TCP")
	}
}
