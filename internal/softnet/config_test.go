// internal/softnet/config_test.go
package softnet

import (
	"encoding/json"
	"reflect"
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
	if !reflect.DeepEqual(back, orig) {
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

func TestForwardTargets_Roundtrip_MacPortsAndPeerHairpins(t *testing.T) {
	in := ControlMsg{
		Op:     "setPolicy",
		Policy: "FORWARDING",
		ForwardTargets: &ForwardTargets{
			HTTP: "127.0.0.1:1", HTTPS: "127.0.0.1:2", DNS: "127.0.0.1:3", NTP: "127.0.0.1:4",
			MacPorts:     []int{3000, 5432},
			PeerHairpins: map[string]string{"sibling.test": "127.0.0.1:60001"},
		},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out ControlMsg
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out.ForwardTargets.MacPorts, in.ForwardTargets.MacPorts) {
		t.Fatalf("MacPorts roundtrip: want %v got %v", in.ForwardTargets.MacPorts, out.ForwardTargets.MacPorts)
	}
	if !reflect.DeepEqual(out.ForwardTargets.PeerHairpins, in.ForwardTargets.PeerHairpins) {
		t.Fatalf("PeerHairpins roundtrip: want %v got %v", in.ForwardTargets.PeerHairpins, out.ForwardTargets.PeerHairpins)
	}
}

func TestForwardTargets_OmitEmpty(t *testing.T) {
	in := ControlMsg{
		Op:     "setPolicy",
		Policy: "FORWARDING",
		ForwardTargets: &ForwardTargets{
			HTTP:  "127.0.0.1:1",
			HTTPS: "127.0.0.1:2",
			DNS:   "127.0.0.1:3",
			NTP:   "127.0.0.1:4",
		},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, k := range []string{"mac_ports", "peer_hairpins"} {
		if strings.Contains(s, k) {
			t.Fatalf("expected %q omitted, got %s", k, s)
		}
	}
}
