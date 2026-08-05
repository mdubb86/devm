package softnet

import "testing"

func TestEgressTarget(t *testing.T) {
	ft := &ForwardTargets{
		HTTP:       "127.0.0.1:8080",
		HTTPS:      "127.0.0.1:8443",
		DNS:        "127.0.0.1:8053",
		NTP:        "127.0.0.1:8123",
		GuestHTTP:  "127.0.0.1:9080",
		GuestHTTPS: "127.0.0.1:9443",
	}
	e := newEgress(nil)

	// LOCKED: everything denied.
	e.setPolicy(PolicyLocked, ft)
	if _, ok := e.target("1.2.3.4", 443); ok {
		t.Fatal("LOCKED must deny :443")
	}

	// OPEN: forward direct to the original dst:port.
	e.setPolicy(PolicyOpen, ft)
	if got, ok := e.target("1.2.3.4", 443); !ok || got != "1.2.3.4:443" {
		t.Fatalf("OPEN :443 = %q,%v want 1.2.3.4:443,true", got, ok)
	}
	if got, ok := e.target("9.9.9.9", 12345); !ok || got != "9.9.9.9:12345" {
		t.Fatalf("OPEN arbitrary port must pass direct, got %q,%v", got, ok)
	}
	if got, ok := e.target(NATAliasIP, 5432); !ok || got != HostLoopIP+":5432" {
		t.Fatalf("OPEN NAT-alias dst = %q,%v want %s:5432", got, ok, HostLoopIP)
	}

	// ENFORCED: :80/:443 -> iron-proxy; other ports denied.
	e.setPolicy(PolicyEnforced, ft)
	if got, ok := e.target("192.0.2.1", 443); !ok || got != ft.HTTPS {
		t.Fatalf("ENFORCED :443 = %q,%v want %s", got, ok, ft.HTTPS)
	}
	if got, ok := e.target("192.0.2.1", 80); !ok || got != ft.HTTP {
		t.Fatalf("ENFORCED :80 = %q,%v want %s", got, ok, ft.HTTP)
	}
	if _, ok := e.target("192.0.2.1", 5432); ok {
		t.Fatal("ENFORCED must deny non-80/443 TCP")
	}
}

// TestEgressTargetInterceptedTest pins .test hairpin routing: the guest-origin
// listener is reached under both OPEN and ENFORCED (so `.test` works during
// the provisioning window), never dialed directly, and denied under LOCKED.
// Non-80/443 ports on the hairpin address are denied — direct services are
// the DNS answer's job (they resolve to 127.0.0.1), not the forwarder's.
func TestEgressTargetInterceptedTest(t *testing.T) {
	ft := &ForwardTargets{
		HTTP:       "127.0.0.1:8080",
		HTTPS:      "127.0.0.1:8443",
		GuestHTTP:  "127.0.0.1:9080",
		GuestHTTPS: "127.0.0.1:9443",
	}
	e := newEgress(nil)

	for _, pol := range []Policy{PolicyOpen, PolicyEnforced} {
		e.setPolicy(pol, ft)
		if got, ok := e.target(InterceptedTestIP, 443); !ok || got != ft.GuestHTTPS {
			t.Fatalf("%s .test:443 = %q,%v want %s", pol, got, ok, ft.GuestHTTPS)
		}
		if got, ok := e.target(InterceptedTestIP, 80); !ok || got != ft.GuestHTTP {
			t.Fatalf("%s .test:80 = %q,%v want %s", pol, got, ok, ft.GuestHTTP)
		}
		if got, _ := e.target(InterceptedTestIP, 443); got == InterceptedTestIP+":443" {
			t.Fatalf("%s must not dial %s directly", pol, InterceptedTestIP)
		}
		if _, ok := e.target(InterceptedTestIP, 5432); ok {
			t.Fatalf("%s must deny .test:5432", pol)
		}
	}

	e.setPolicy(PolicyLocked, ft)
	if _, ok := e.target(InterceptedTestIP, 443); ok {
		t.Fatal("LOCKED must deny .test:443")
	}

	// No guest-origin targets configured => denied, not a panic.
	e2 := newEgress(nil)
	e2.setPolicy(PolicyEnforced, &ForwardTargets{HTTP: "127.0.0.1:8080"})
	if _, ok := e2.target(InterceptedTestIP, 80); ok {
		t.Fatal("empty GuestHTTP must deny")
	}
}
