package softnet

import (
	"encoding/json"
	"testing"

	"github.com/mdubb86/devm/internal/identity"
)

func TestApplyControlSetPolicy(t *testing.T) {
	e := newEgress(nil)
	_, err := applyControl(e, newIngress(identity.Prod, nil), ControlMsg{
		Op:             "setPolicy",
		Policy:         "FORWARDING",
		ForwardTargets: &ForwardTargets{HTTPS: "127.0.0.1:8443"},
	}, nil)
	if err != nil {
		t.Fatalf("applyControl: %v", err)
	}
	if got, ok := e.target("192.0.2.1", 443); !ok || got != "127.0.0.1:8443" {
		t.Fatalf("after FORWARDING apply, :443 = %q,%v", got, ok)
	}
}

func TestApplyControlUnknownOpIgnored(t *testing.T) {
	e := newEgress(nil)
	if _, err := applyControl(e, newIngress(identity.Prod, nil), ControlMsg{Op: "bogus"}, nil); err != nil {
		t.Fatalf("unknown op must be ignored, got %v", err)
	}
}

func TestApplyControlSetExposeMap(t *testing.T) {
	ing := newIngress(identity.Prod, nil)
	p := freeTCPPort(t)
	reply, err := applyControl(newEgress(nil), ing, ControlMsg{
		Op:     "setExposeMap",
		Expose: []ExposePort{{GuestPort: 5432, BindIP: "127.0.0.1", HostPort: p}},
	}, nil)
	if err != nil {
		t.Fatalf("applyControl setExposeMap: %v", err)
	}
	if !hostReachable(p) {
		t.Fatalf("setExposeMap should have opened host port %d", p)
	}
	// setExposeMap replies with a per-port ack the daemon reads.
	var ack ExposeAck
	if err := json.Unmarshal(reply, &ack); err != nil {
		t.Fatalf("reply is not an ExposeAck: %v (%s)", err, reply)
	}
	if !ack.OK || len(ack.Results) != 1 || !ack.Results[0].OK || ack.Results[0].HostPort != p {
		t.Fatalf("unexpected ack: %+v", ack)
	}
	ing.close()
}

// Ops other than setExposeMap reply with nothing — their senders
// close the connection without reading.
func TestApplyControlNonExposeOpsReplyNil(t *testing.T) {
	reply, err := applyControl(newEgress(nil), newIngress(identity.Prod, nil), ControlMsg{
		Op: "setTestHosts", DirectTestHosts: []string{"db.test"},
	}, nil)
	if err != nil {
		t.Fatalf("applyControl: %v", err)
	}
	if reply != nil {
		t.Fatalf("setTestHosts must not reply, got %s", reply)
	}
}

// TestApplyControlShutdownInvokesCallback locks the wiring /vm/stop relies
// on: a "shutdown" control message must invoke the caller-supplied callback
// (Run in softnet.go passes its own signal.NotifyContext cancel func) so the
// process can exit even though it's a child `tart run --net-softnet` forks
// internally and the daemon's process supervisor never signals it directly.
func TestApplyControlShutdownInvokesCallback(t *testing.T) {
	called := false
	_, err := applyControl(newEgress(nil), newIngress(identity.Prod, nil), ControlMsg{
		Op: "shutdown",
	}, func() { called = true })
	if err != nil {
		t.Fatalf("applyControl shutdown: %v", err)
	}
	if !called {
		t.Fatal("shutdown op must invoke the shutdown callback")
	}
}

// TestControlSetTestHosts pins the wire shape of the setTestHosts op.
func TestControlSetTestHosts(t *testing.T) {
	var m ControlMsg
	line := `{"op":"setTestHosts","direct_test_hosts":["db.test","cache.test"]}`
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m.Op != "setTestHosts" || len(m.DirectTestHosts) != 2 || m.DirectTestHosts[0] != "db.test" {
		t.Fatalf("decoded %+v", m)
	}
}

// TestApplyControlShutdownNilCallbackDoesNotPanic covers serveControl's own
// call sites (Run only wires a callback when SOFTNET_CONTROL_SOCK is set);
// a nil callback must be a safe no-op rather than a nil-deref panic.
func TestApplyControlShutdownNilCallbackDoesNotPanic(t *testing.T) {
	_, err := applyControl(newEgress(nil), newIngress(identity.Prod, nil), ControlMsg{
		Op: "shutdown",
	}, nil)
	if err != nil {
		t.Fatalf("applyControl shutdown with nil callback: %v", err)
	}
}
