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

// startTestDNS spins up the DNS server on an ephemeral port and
// returns its address. Test code dials that port directly.
func startTestDNS(t *testing.T) (string, func()) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := pc.LocalAddr().String()
	_ = pc.Close()

	s := newDNSServerAt(addr)
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

func TestDNS_TestTLD_AAAA_ReturnsNoData(t *testing.T) {
	server, cleanup := startTestDNS(t)
	defer cleanup()
	reply := queryUDP(t, server, "anything.test", dns.TypeAAAA)
	assert.Empty(t, reply.Answer, "AAAA queries should return NODATA (empty Answer)")
	assert.Equal(t, dns.RcodeSuccess, reply.Rcode, "rcode must be NoError for NODATA")
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
	s := newDNSServerAt(addr)
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

func TestDNSAnswersLoopbackRegardlessOfRoute(t *testing.T) {
	s := newDNSServerAt("127.0.0.1:0")

	assertA := func(name string) {
		t.Helper()
		req := new(dns.Msg)
		req.SetQuestion(dns.Fqdn(name), dns.TypeA)
		rec := &testResponseWriter{}
		s.handleTest(rec, req)
		require.Len(t, rec.msg.Answer, 1)
		a := rec.msg.Answer[0].(*dns.A)
		assert.Equal(t, "127.0.0.1", a.A.String())
		assert.Equal(t, uint32(0), a.Hdr.Ttl, "all answers must be TTL 0")
	}
	assertA("db.test")     // direct → loopback (softnet exposes the port on the host)
	assertA("web.test")    // proxied → loopback (unchanged)
	assertA("random.test") // unknown → loopback (unchanged)
}

func TestHandleTest_DirectServiceAnswersLoopback(t *testing.T) {
	// Ingress flows entirely through softnet's host-side listeners —
	// every .test name must answer loopback.
	s := newDNSServerAt("127.0.0.1:0")

	a := new(dns.Msg)
	a.SetQuestion(dns.Fqdn("db.test"), dns.TypeA)
	recA := &testResponseWriter{}
	s.handleTest(recA, a)
	require.Len(t, recA.msg.Answer, 1)
	arec, ok := recA.msg.Answer[0].(*dns.A)
	require.True(t, ok, "expected A record")
	assert.Equal(t, "127.0.0.1", arec.A.String())

	aaaa := new(dns.Msg)
	aaaa.SetQuestion(dns.Fqdn("db.test"), dns.TypeAAAA)
	recAAAA := &testResponseWriter{}
	s.handleTest(recAAAA, aaaa)
	assert.Empty(t, recAAAA.msg.Answer, "AAAA queries should return NODATA (empty Answer)")
	assert.Equal(t, dns.RcodeSuccess, recAAAA.msg.Rcode, "rcode must be NoError for NODATA")
}
