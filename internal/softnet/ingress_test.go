package softnet

import (
	"net"
	"strconv"
	"testing"
	"time"
)

// itoa converts an int to its decimal string form.
func itoa(i int) string { return strconv.Itoa(i) }

// freeTCPPort binds 127.0.0.1:0, reads the OS-assigned port, and releases it.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freeTCPPort: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// hostReachable reports whether something is accepting on 127.0.0.1:port.
func hostReachable(port int) bool {
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", itoa(port)), 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func TestIngressApplyReconciles(t *testing.T) {
	ing := newIngress(nil) // apply's listener lifecycle doesn't need a live stack
	p1, p2 := freeTCPPort(t), freeTCPPort(t)

	ing.apply([]ExposePort{{GuestPort: 5432, BindIP: "127.0.0.1", HostPort: p1}})
	if !hostReachable(p1) {
		t.Fatalf("after apply, host port %d should be listening", p1)
	}

	// Reconcile: replace p1 with p2.
	ing.apply([]ExposePort{{GuestPort: 5432, BindIP: "127.0.0.1", HostPort: p2}})
	if hostReachable(p1) {
		t.Fatalf("p1 %d should be closed after reconcile", p1)
	}
	if !hostReachable(p2) {
		t.Fatalf("p2 %d should be open after reconcile", p2)
	}

	ing.close()
	if hostReachable(p2) {
		t.Fatalf("p2 %d should be closed after close()", p2)
	}
}
