package serviceapi

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mdubb86/devm/internal/identity"
)

// fixedLookup returns a lookup function for a single project name.
func fixedLookup(project, ip string) func(string) (string, bool) {
	return func(p string) (string, bool) {
		if p == project {
			return ip, true
		}
		return "", false
	}
}

// startTestDNS spins up the DNS server on an ephemeral port and
// returns its address. Test code dials that port directly.
func startTestDNS(t *testing.T, lookup func(string) (string, bool)) (string, func()) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := pc.LocalAddr().String()
	_ = pc.Close()

	s := newDNSServerAt(identity.Prod, addr, lookup)
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

func TestDNS_TestTLD_A_AnswersProjectIP(t *testing.T) {
	server, cleanup := startTestDNS(t, fixedLookup("myapp", "127.42.0.1"))
	defer cleanup()
	reply := queryUDP(t, server, "myapp.test", dns.TypeA)
	require.Len(t, reply.Answer, 1)
	a, ok := reply.Answer[0].(*dns.A)
	require.True(t, ok, "expected A record")
	assert.Equal(t, "127.42.0.1", a.A.String())
}

func TestDNS_TestTLD_UnknownProject_NXDOMAIN(t *testing.T) {
	server, cleanup := startTestDNS(t, fixedLookup("myapp", "127.42.0.1"))
	defer cleanup()
	reply := queryUDP(t, server, "anything.test", dns.TypeA)
	assert.Equal(t, dns.RcodeNameError, reply.Rcode)
	assert.Empty(t, reply.Answer)
}

func TestDNS_TestTLD_AAAA_ReturnsNoData(t *testing.T) {
	server, cleanup := startTestDNS(t, fixedLookup("myapp", "127.42.0.1"))
	defer cleanup()
	reply := queryUDP(t, server, "myapp.test", dns.TypeAAAA)
	assert.Empty(t, reply.Answer, "AAAA queries should return NODATA (empty Answer)")
	assert.Equal(t, dns.RcodeSuccess, reply.Rcode, "rcode must be NoError for NODATA")
}

func TestDNS_TestTLD_MX_NoData(t *testing.T) {
	server, cleanup := startTestDNS(t, fixedLookup("myapp", "127.42.0.1"))
	defer cleanup()
	reply := queryUDP(t, server, "myapp.test", dns.TypeMX)
	assert.Empty(t, reply.Answer, "MX queries should return NODATA (empty Answer)")
	assert.Equal(t, dns.RcodeSuccess, reply.Rcode, "rcode must be NoError for NODATA")
}

func TestDNS_DeepSubdomain_ResolvesToOwningProject(t *testing.T) {
	server, cleanup := startTestDNS(t, fixedLookup("foo", "127.42.0.5"))
	defer cleanup()
	reply := queryUDP(t, server, "a.b.c.d.e.foo.test", dns.TypeA)
	require.Len(t, reply.Answer, 1)
	a := reply.Answer[0].(*dns.A)
	assert.Equal(t, "127.42.0.5", a.A.String())
}

func TestDNS_PortInUse_ReturnsError(t *testing.T) {
	// Bind a UDP listener to claim a port.
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	defer pc.Close()
	addr := pc.LocalAddr().String()

	// Now try to start our DNS server on the same port.
	s := newDNSServerAt(identity.Prod, addr, fixedLookup("myapp", "127.42.0.1"))
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

func TestDNS_AnswersProjectIP(t *testing.T) {
	// Build a server pointed at an in-memory lookup that returns
	// 127.42.0.1 for "myapp" and (empty, false) for anything else.
	lookup := func(name string) (string, bool) {
		if name == "myapp" {
			return "127.42.0.1", true
		}
		return "", false
	}
	s := newDNSServerAt(identity.Prod, "127.0.0.1:0", lookup)
	// Testing the handler directly avoids the ListenAndServe dance.
	msg := new(dns.Msg)
	msg.SetQuestion("api.myapp.test.", dns.TypeA)
	w := &memWriter{}
	s.handleTest(w, msg)
	require.Len(t, w.msg.Answer, 1)
	a, ok := w.msg.Answer[0].(*dns.A)
	require.True(t, ok)
	assert.Equal(t, "127.42.0.1", a.A.String())
}

func TestDNS_NXDOMAIN_UnknownProject(t *testing.T) {
	lookup := func(name string) (string, bool) { return "", false }
	s := newDNSServerAt(identity.Prod, "127.0.0.1:0", lookup)
	msg := new(dns.Msg)
	msg.SetQuestion("foo.unknown.test.", dns.TypeA)
	w := &memWriter{}
	s.handleTest(w, msg)
	assert.Equal(t, dns.RcodeNameError, w.msg.Rcode)
	assert.Empty(t, w.msg.Answer)
}

type memWriter struct {
	msg *dns.Msg
	dns.ResponseWriter
}

func (m *memWriter) WriteMsg(msg *dns.Msg) error {
	m.msg = msg
	return nil
}

func TestExtractProjectLabel(t *testing.T) {
	cases := map[string]string{
		"myapp.test.":         "myapp",
		"api.myapp.test.":     "myapp",
		"foo.bar.myapp.test.": "myapp",
		"MyApp.test.":         "myapp", // case-insensitive
	}
	for in, want := range cases {
		assert.Equal(t, want, extractProjectLabel(in, identity.Prod.TLD), "input %q", in)
	}
}

func TestDNS_HealthCheckSentinel_AlwaysAnswersLoopback(t *testing.T) {
	// devm-health-check.test isn't a real project — CheckDNSHealth
	// (used by `devm status`) relies on it always resolving to
	// 127.0.0.1 regardless of what's currently allocated, so a "no
	// projects running" daemon still reports DNS healthy.
	s := newDNSServerAt(identity.Prod, "127.0.0.1:0", func(string) (string, bool) { return "", false })
	msg := new(dns.Msg)
	msg.SetQuestion(dns.Fqdn(dnsProbeName(identity.Prod)), dns.TypeA)
	w := &memWriter{}
	s.handleTest(w, msg)
	require.Len(t, w.msg.Answer, 1)
	a, ok := w.msg.Answer[0].(*dns.A)
	require.True(t, ok)
	assert.Equal(t, "127.0.0.1", a.A.String())
	assert.Equal(t, dns.RcodeSuccess, w.msg.Rcode)
}

func TestHandleTest_UnknownProjectAnswersNXDOMAINNotLoopback(t *testing.T) {
	// Regression pin: pre-B3 behavior answered 127.0.0.1 for every
	// *.test name regardless of project. Post-B3, only a known,
	// running project's own ProjectIP resolves; everything else is
	// NXDOMAIN — the isolation guarantee.
	s := newDNSServerAt(identity.Prod, "127.0.0.1:0", fixedLookup("myapp", "127.42.0.1"))

	a := new(dns.Msg)
	a.SetQuestion(dns.Fqdn("db.myapp.test"), dns.TypeA)
	recA := &testResponseWriter{}
	s.handleTest(recA, a)
	require.Len(t, recA.msg.Answer, 1)
	arec, ok := recA.msg.Answer[0].(*dns.A)
	require.True(t, ok, "expected A record")
	assert.Equal(t, "127.42.0.1", arec.A.String())

	unknown := new(dns.Msg)
	unknown.SetQuestion(dns.Fqdn("db.otherapp.test"), dns.TypeA)
	recUnknown := &testResponseWriter{}
	s.handleTest(recUnknown, unknown)
	assert.Empty(t, recUnknown.msg.Answer)
	assert.Equal(t, dns.RcodeNameError, recUnknown.msg.Rcode)
}
