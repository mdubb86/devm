package serviceapi

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// noopResolver never resolves a project's VM IP — used by tests that
// exercise the plain 127.0.0.1 behavior with no direct routes.
func noopResolver(string) (string, bool) { return "", false }

// startTestDNS spins up the DNS server on an ephemeral port and
// returns its address. Test code dials that port directly.
func startTestDNS(t *testing.T) (string, func()) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := pc.LocalAddr().String()
	_ = pc.Close()

	s := newDNSServerAt(addr, NewRoutes(), noopResolver)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- s.Serve(ctx) }()

	// Wait briefly for the server to bind.
	time.Sleep(100 * time.Millisecond)

	return addr, func() {
		cancel()
		<-errCh
	}
}

func queryUDP(t *testing.T, server, name string, qtype uint16) *dns.Msg {
	t.Helper()
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), qtype)
	c := &dns.Client{Net: "udp", Timeout: 2 * time.Second}
	reply, _, err := c.Exchange(m, server)
	require.NoError(t, err)
	return reply
}

func TestDNS_TestTLD_A_Returns127001(t *testing.T) {
	server, cleanup := startTestDNS(t)
	defer cleanup()
	reply := queryUDP(t, server, "anything.test", dns.TypeA)
	require.Len(t, reply.Answer, 1)
	a, ok := reply.Answer[0].(*dns.A)
	require.True(t, ok, "expected A record")
	assert.Equal(t, "127.0.0.1", a.A.String())
}

func TestDNS_TestTLD_AAAA_ReturnsLoopbackV6(t *testing.T) {
	server, cleanup := startTestDNS(t)
	defer cleanup()
	reply := queryUDP(t, server, "anything.test", dns.TypeAAAA)
	require.Len(t, reply.Answer, 1)
	aaaa, ok := reply.Answer[0].(*dns.AAAA)
	require.True(t, ok, "expected AAAA record")
	assert.Equal(t, "::1", aaaa.AAAA.String())
}

func TestDNS_TestTLD_MX_NoData(t *testing.T) {
	server, cleanup := startTestDNS(t)
	defer cleanup()
	reply := queryUDP(t, server, "anything.test", dns.TypeMX)
	assert.Empty(t, reply.Answer, "MX queries should return NODATA (empty Answer)")
	assert.Equal(t, dns.RcodeSuccess, reply.Rcode, "rcode must be NoError for NODATA")
}

func TestDNS_DeepSubdomain_Resolves(t *testing.T) {
	server, cleanup := startTestDNS(t)
	defer cleanup()
	reply := queryUDP(t, server, "a.b.c.d.e.foo.test", dns.TypeA)
	require.Len(t, reply.Answer, 1)
	a := reply.Answer[0].(*dns.A)
	assert.Equal(t, "127.0.0.1", a.A.String())
}

func TestDNS_PortInUse_ReturnsError(t *testing.T) {
	// Bind a UDP listener to claim a port.
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	defer pc.Close()
	addr := pc.LocalAddr().String()

	// Now try to start our DNS server on the same port.
	s := newDNSServerAt(addr, NewRoutes(), noopResolver)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = s.Serve(ctx)
	require.Error(t, err, "should fail to bind on a busy port")
}

// testResponseWriter is a minimal dns.ResponseWriter that records the
// message passed to WriteMsg; used to drive handleTest directly
// without a real UDP round trip.
type testResponseWriter struct{ msg *dns.Msg }

func (w *testResponseWriter) WriteMsg(m *dns.Msg) error { w.msg = m; return nil }
func (w *testResponseWriter) LocalAddr() net.Addr       { return &net.UDPAddr{} }
func (w *testResponseWriter) RemoteAddr() net.Addr      { return &net.UDPAddr{} }
func (w *testResponseWriter) Write([]byte) (int, error) { return 0, nil }
func (w *testResponseWriter) Close() error              { return nil }
func (w *testResponseWriter) TsigStatus() error         { return nil }
func (w *testResponseWriter) TsigTimersOnly(bool)       {}
func (w *testResponseWriter) Hijack()                   {}

func TestDNSAnswersVMIPForDirect(t *testing.T) {
	routes := NewRoutes()
	routes.Apply("proj", []Route{
		{Hostname: "db.test", BackendPort: 54322, Direct: true, Project: "proj"},
		{Hostname: "web.test", BackendPort: 8080, Mode: ModeVM, Project: "proj"},
	})
	resolver := func(project string) (string, bool) {
		if project == "proj" {
			return "192.168.64.4", true
		}
		return "", false
	}
	s := newDNSServerAt("127.0.0.1:0", routes, resolver)

	assertA := func(name, wantIP string) {
		t.Helper()
		req := new(dns.Msg)
		req.SetQuestion(dns.Fqdn(name), dns.TypeA)
		rec := &testResponseWriter{}
		s.handleTest(rec, req)
		require.Len(t, rec.msg.Answer, 1)
		a := rec.msg.Answer[0].(*dns.A)
		assert.Equal(t, wantIP, a.A.String())
		assert.Equal(t, uint32(0), a.Hdr.Ttl, "all answers must be TTL 0")
	}
	assertA("db.test", "192.168.64.4")  // direct → VM IP
	assertA("web.test", "127.0.0.1")    // proxied → loopback
	assertA("random.test", "127.0.0.1") // unknown → loopback (unchanged)

	// AAAA for a direct host is NODATA (v4-only) so a v6-capable client
	// falls back to the A-record VM IP instead of ::1 (Mac loopback).
	aaaa := new(dns.Msg)
	aaaa.SetQuestion(dns.Fqdn("db.test"), dns.TypeAAAA)
	rec := &testResponseWriter{}
	s.handleTest(rec, aaaa)
	assert.Empty(t, rec.msg.Answer, "direct AAAA must be NODATA")
}
