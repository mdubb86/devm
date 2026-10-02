// internal/softnet/config_test.go
package softnet

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPolicyString(t *testing.T) {
	cases := map[Policy]string{PolicyLocked: "LOCKED", PolicyForwarding: "FORWARDING"}
	for p, want := range cases {
		if got := p.String(); got != want {
			t.Fatalf("Policy(%d).String() = %q, want %q", int(p), got, want)
		}
	}
}

func TestParsePolicy(t *testing.T) {
	p, err := ParsePolicy("FORWARDING")
	if err != nil || p != PolicyForwarding {
		t.Fatalf("ParsePolicy(FORWARDING) = %v, %v", p, err)
	}
	if _, err := ParsePolicy("bogus"); err == nil {
		t.Fatal("ParsePolicy(bogus) should error")
	}
	if _, err := ParsePolicy("OPEN"); err == nil {
		t.Fatal("ParsePolicy(OPEN) should error")
	}
}

// TestForwardTargets_GuestAPIField_JSONRoundtrip pins that GuestAPI
// round-trips through JSON with the "guest_api" tag so the daemon can
// push it via setPolicy.
func TestForwardTargets_GuestAPIField_JSONRoundtrip(t *testing.T) {
	orig := ForwardTargets{
		HTTP:     "127.0.0.1:1000",
		HTTPS:    "127.0.0.1:1001",
		DNS:      "127.0.0.1:1002",
		NTP:      "127.0.0.1:1003",
		GuestAPI: "127.0.0.1:1004",
	}
	blob, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(blob), `"guest_api":"127.0.0.1:1004"`) {
		t.Fatalf("marshaled JSON missing guest_api field: %s", blob)
	}
	var back ForwardTargets
	if err := json.Unmarshal(blob, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if back != orig {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", back, orig)
	}
}

// TestForwardTargets_GuestAPIOmittedWhenEmpty pins that GuestAPI is
// optional — an unset field must not appear in the JSON.
func TestForwardTargets_GuestAPIOmittedWhenEmpty(t *testing.T) {
	minimal := ForwardTargets{HTTP: "127.0.0.1:1", HTTPS: "127.0.0.1:2", DNS: "127.0.0.1:3", NTP: "127.0.0.1:4"}
	blob, err := json.Marshal(minimal)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(blob), "guest_api") {
		t.Fatalf("marshaled JSON should omit unset guest_api field: %s", blob)
	}
}
