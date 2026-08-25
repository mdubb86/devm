package softnet

import (
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

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

// TestEgress_ForwardsPopPortToPopEndpoint pins that a guest TCP flow to
// the gateway's pop port (192.168.127.1:81) routes to ForwardTargets.Pop
// when set. Serves the daemon's per-project pop HTTP listener; see
// internal/serviceapi/pop.go for the handler.
func TestEgress_ForwardsPopPortToPopEndpoint(t *testing.T) {
	ft := &ForwardTargets{
		HTTP: "127.0.0.1:8080", HTTPS: "127.0.0.1:8443",
		DNS: "127.0.0.1:8053", NTP: "127.0.0.1:8123",
		Pop: "127.0.0.1:65431",
	}
	e := newEgress(nil)
	e.setPolicy(PolicyEnforced, ft)

	got, ok := e.target(GatewayIP, 81)
	if !ok {
		t.Fatal("TCP:81 to gateway must forward under ENFORCED with Pop set")
	}
	if got != "127.0.0.1:65431" {
		t.Fatalf("target(gateway, 81) = %q, want 127.0.0.1:65431", got)
	}
}

// TestEgress_DoesNotForwardPopPortWhenPopUnset pins that the gateway's
// pop port is denied, not silently forwarded elsewhere, when Pop hasn't
// been configured.
func TestEgress_DoesNotForwardPopPortWhenPopUnset(t *testing.T) {
	ft := &ForwardTargets{
		HTTP: "127.0.0.1:8080", HTTPS: "127.0.0.1:8443",
		DNS: "127.0.0.1:8053", NTP: "127.0.0.1:8123",
		// Pop deliberately omitted
	}
	e := newEgress(nil)
	e.setPolicy(PolicyEnforced, ft)

	if _, ok := e.target(GatewayIP, 81); ok {
		t.Fatal("TCP:81 must NOT forward when Pop is unset")
	}
}

// TestSpliceReturnsByteCounts pins that splice reports how many bytes
// flowed each direction — the counts callers log to detect splices
// that established but never carried data.
func TestSpliceReturnsByteCounts(t *testing.T) {
	aClient, aSplice := net.Pipe()
	bClient, bSplice := net.Pipe()

	type result struct{ aRead, bRead int64 }
	done := make(chan result, 1)
	go func() {
		aRead, bRead, _, _ := splice(aSplice, bSplice)
		done <- result{aRead, bRead}
	}()

	// a -> b: write 5 bytes into aClient, read them out of bClient.
	go func() { _, _ = aClient.Write([]byte("hello")) }()
	got := make([]byte, 5)
	if _, err := io.ReadFull(bClient, got); err != nil {
		t.Fatalf("read from bClient: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("a->b payload = %q, want hello", got)
	}

	// b -> a: write 6 bytes into bClient, read them out of aClient.
	go func() { _, _ = bClient.Write([]byte("world!")) }()
	got2 := make([]byte, 6)
	if _, err := io.ReadFull(aClient, got2); err != nil {
		t.Fatalf("read from aClient: %v", err)
	}
	if string(got2) != "world!" {
		t.Fatalf("b->a payload = %q, want world!", got2)
	}

	// Closing both client ends unwinds both io.Copy loops.
	_ = aClient.Close()
	_ = bClient.Close()

	r := <-done
	if r.aRead != 5 {
		t.Errorf("aRead = %d, want 5", r.aRead)
	}
	if r.bRead != 6 {
		t.Errorf("bRead = %d, want 6", r.bRead)
	}
}

// TestSpliceZeroBytesBothWays pins the anomaly signal used by the
// egress log gate: when both ends close without sending, both byte
// counts are zero.
func TestSpliceZeroBytesBothWays(t *testing.T) {
	aClient, aSplice := net.Pipe()
	bClient, bSplice := net.Pipe()

	// Close both client ends before splice sees any data.
	_ = aClient.Close()
	_ = bClient.Close()

	aRead, bRead, _, _ := splice(aSplice, bSplice)
	if aRead != 0 || bRead != 0 {
		t.Fatalf("aRead=%d bRead=%d, want 0/0", aRead, bRead)
	}
}

// TestRealErr filters the errors io.Copy uses to signal clean
// end-of-stream, so a normal disconnect doesn't drown out the 0-byte
// signal at the log gate.
func TestRealErr(t *testing.T) {
	other := errors.New("boom")
	cases := []struct {
		name string
		in   error
		want error
	}{
		{"nil", nil, nil},
		{"eof", io.EOF, nil},
		{"wrapped-eof", fmt.Errorf("read: %w", io.EOF), nil},
		{"net-closed", net.ErrClosed, nil},
		{"wrapped-net-closed", fmt.Errorf("write: %w", net.ErrClosed), nil},
		{"real", other, other},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := realErr(c.in); got != c.want {
				t.Fatalf("realErr(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// halfCloseSpy is a net.Conn that records which of CloseWrite/CloseRead
// were invoked, so we can pin that halfClose (and therefore splice)
// signals both sides on each direction's end.
type halfCloseSpy struct {
	net.Conn
	closedWrite bool
	closedRead  bool
}

func (h *halfCloseSpy) CloseWrite() error { h.closedWrite = true; return nil }
func (h *halfCloseSpy) CloseRead() error  { h.closedRead = true; return nil }

// TestHalfCloseSignalsBothSides pins the tcpproxy/tailscale pattern
// splice relies on: after one direction's io.Copy ends, dst gets
// CloseWrite and src gets CloseRead — CloseRead unblocks a peer that
// stopped reading its half while we were still writing.
func TestHalfCloseSignalsBothSides(t *testing.T) {
	dst := &halfCloseSpy{Conn: nopConn{}}
	src := &halfCloseSpy{Conn: nopConn{}}
	halfClose(dst, src)
	if !dst.closedWrite {
		t.Error("dst.CloseWrite not called")
	}
	if !src.closedRead {
		t.Error("src.CloseRead not called")
	}
	if dst.closedRead || src.closedWrite {
		t.Error("halfClose crossed sides: dst got CloseRead or src got CloseWrite")
	}
}

// nopConn satisfies net.Conn for embedding in test-only types that
// only need to satisfy the interface — the spy provides the methods
// under test.
type nopConn struct{}

func (nopConn) Read(_ []byte) (int, error)         { return 0, io.EOF }
func (nopConn) Write(p []byte) (int, error)        { return len(p), nil }
func (nopConn) Close() error                       { return nil }
func (nopConn) LocalAddr() net.Addr                { return nil }
func (nopConn) RemoteAddr() net.Addr               { return nil }
func (nopConn) SetDeadline(_ time.Time) error      { return nil }
func (nopConn) SetReadDeadline(_ time.Time) error  { return nil }
func (nopConn) SetWriteDeadline(_ time.Time) error { return nil }
