// internal/softnet/config_test.go
package softnet

import "testing"

func TestPolicyString(t *testing.T) {
	cases := map[Policy]string{PolicyLocked: "LOCKED", PolicyOpen: "OPEN", PolicyEnforced: "ENFORCED"}
	for p, want := range cases {
		if got := p.String(); got != want {
			t.Fatalf("Policy(%d).String() = %q, want %q", int(p), got, want)
		}
	}
}

func TestParsePolicy(t *testing.T) {
	p, err := ParsePolicy("ENFORCED")
	if err != nil || p != PolicyEnforced {
		t.Fatalf("ParsePolicy(ENFORCED) = %v, %v", p, err)
	}
	if _, err := ParsePolicy("bogus"); err == nil {
		t.Fatal("ParsePolicy(bogus) should error")
	}
}
