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
	}, nil)
	if err != nil {
		t.Fatalf("applyControl: %v", err)
	}
	if got, ok := e.target("192.0.2.1", 443); !ok || got != "127.0.0.1:8443" {
		t.Fatalf("after ENFORCED apply, :443 = %q,%v", got, ok)
	}
}

func TestApplyControlUnknownOpIgnored(t *testing.T) {
	e := newEgress(nil)
	if err := applyControl(e, newIngress(identity.Prod, nil), ControlMsg{Op: "bogus"}, nil); err != nil {
		t.Fatalf("unknown op must be ignored, got %v", err)
	}
}

func TestApplyControlSetExposeMap(t *testing.T) {
	ing := newIngress(identity.Prod, nil)
	p := freeTCPPort(t)
	err := applyControl(newEgress(nil), ing, ControlMsg{
		Op:     "setExposeMap",
		Expose: []ExposePort{{GuestPort: 5432, BindIP: "127.0.0.1", HostPort: p}},
	}, nil)
	if err != nil {
		t.Fatalf("applyControl setExposeMap: %v", err)
	}
	if !hostReachable(p) {
		t.Fatalf("setExposeMap should have opened host port %d", p)
	}
	ing.close()
}

// TestApplyControlShutdownInvokesCallback locks the wiring /vm/stop relies
// on: a "shutdown" control message must invoke the caller-supplied callback
// (Run in softnet.go passes its own signal.NotifyContext cancel func) so the
// process can exit even though it's a child `tart run --net-softnet` forks
// internally and the daemon's process supervisor never signals it directly.
func TestApplyControlShutdownInvokesCallback(t *testing.T) {
	called := false
	err := applyControl(newEgress(nil), newIngress(identity.Prod, nil), ControlMsg{
		Op: "shutdown",
	}, func() { called = true })
	if err != nil {
		t.Fatalf("applyControl shutdown: %v", err)
	}
	if !called {
		t.Fatal("shutdown op must invoke the shutdown callback")
	}
}

// TestApplyControlShutdownNilCallbackDoesNotPanic covers serveControl's own
// call sites (Run only wires a callback when SOFTNET_CONTROL_SOCK is set);
// a nil callback must be a safe no-op rather than a nil-deref panic.
func TestApplyControlShutdownNilCallbackDoesNotPanic(t *testing.T) {
	err := applyControl(newEgress(nil), newIngress(identity.Prod, nil), ControlMsg{
		Op: "shutdown",
	}, nil)
	if err != nil {
		t.Fatalf("applyControl shutdown with nil callback: %v", err)
	}
}
