package softnet

import "testing"

func TestApplyControlSetPolicy(t *testing.T) {
	e := newEgress(nil)
	err := applyControl(e, ControlMsg{
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
	if err := applyControl(e, ControlMsg{Op: "setExposeMap"}); err != nil {
		t.Fatalf("unknown op must be ignored, got %v", err)
	}
}
