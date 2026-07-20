package softnet

import (
	"testing"

	"github.com/mdubb86/devm/internal/identity"
)

func TestApplyControlSetPolicy(t *testing.T) {
	e := newEgress(nil)
	err := applyControl(e, newIngress(identity.Prod, nil), ControlMsg{
		Op:        "setPolicy",
		Policy:    "ENFORCED",
		IronProxy: &IronProxyEndpoint{HTTPS: "127.0.0.1:8443"},
	})
	if err != nil {
		t.Fatalf("applyControl: %v", err)
	}
	if got, ok := e.target("192.0.2.1", 443); !ok || got != "127.0.0.1:8443" {
		t.Fatalf("after ENFORCED apply, :443 = %q,%v", got, ok)
	}
}

func TestApplyControlUnknownOpIgnored(t *testing.T) {
	e := newEgress(nil)
	if err := applyControl(e, newIngress(identity.Prod, nil), ControlMsg{Op: "bogus"}); err != nil {
		t.Fatalf("unknown op must be ignored, got %v", err)
	}
}

func TestApplyControlSetExposeMap(t *testing.T) {
	ing := newIngress(identity.Prod, nil)
	p := freeTCPPort(t)
	err := applyControl(newEgress(nil), ing, ControlMsg{
		Op:     "setExposeMap",
		Expose: []ExposePort{{GuestPort: 5432, BindIP: "127.0.0.1", HostPort: p}},
	})
	if err != nil {
		t.Fatalf("applyControl setExposeMap: %v", err)
	}
	if !hostReachable(p) {
		t.Fatalf("setExposeMap should have opened host port %d", p)
	}
	ing.close()
}
