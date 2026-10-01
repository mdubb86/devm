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

// TestForwardTargets_ProposeField_JSONRoundtrip pins that Propose
// round-trips through JSON when set.
func TestForwardTargets_ProposeField_JSONRoundtrip(t *testing.T) {
	in := &ForwardTargets{
		HTTP:    "127.0.0.1:1000",
		HTTPS:   "127.0.0.1:1001",
		DNS:     "127.0.0.1:1002",
		NTP:     "127.0.0.1:1003",
		Propose: "127.0.0.1:1005",
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out ForwardTargets
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Propose != "127.0.0.1:1005" {
		t.Fatalf("Propose lost: got %q want %q", out.Propose, "127.0.0.1:1005")
	}
}

// TestForwardTargets_ProposeOmittedWhenEmpty pins that Propose is
// optional — an old daemon that doesn't set it produces JSON without
// the field.
func TestForwardTargets_ProposeOmittedWhenEmpty(t *testing.T) {
	in := &ForwardTargets{
		HTTP:  "127.0.0.1:1000",
		HTTPS: "127.0.0.1:1001",
		DNS:   "127.0.0.1:1002",
		NTP:   "127.0.0.1:1003",
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "propose") {
		t.Fatalf("propose should be omitted when empty; got: %s", b)
	}
}
