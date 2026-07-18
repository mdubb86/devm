package serviceapi

import (
	"context"
	"net"
	"os"

	"github.com/miekg/dns"

	"github.com/mdubb86/devm/internal/debuglog"
)

const (
	// defaultDNSAddr is the UDP socket the daemon listens on for
	// *.test queries. Forwarded here by /etc/resolver/test written by
	// `devm install`. Overridable via $DEVM_DNS_ADDR — e2e's isolated
	// mode sets it to 127.0.0.1:0 (ephemeral port) so a second daemon
	// can coexist with the user's real one without a port collision.
	defaultDNSAddr = "127.0.0.1:51153"

	// dnsAddrEnv is the environment variable that overrides
	// defaultDNSAddr. See defaultDNSAddr.
	dnsAddrEnv = "DEVM_DNS_ADDR"

	// testTLD is the only suffix we serve. miekg/dns expects the
	// trailing dot.
	testTLD = "test."
)

// DNSAddr returns the address the daemon's *.test DNS server binds
// to. Respects $DEVM_DNS_ADDR when set; otherwise returns the default
// port that /etc/resolver/test points at.
func DNSAddr() string {
	if v := os.Getenv(dnsAddrEnv); v != "" {
		return v
	}
	return defaultDNSAddr
}

// DNSServer is the daemon's tiny *.test resolver. Every *.test name
// answers 127.0.0.1 (A), TTL 0 — the host-local address where either
// the daemon HTTP proxy (proxied services) or a softnet per-port
// forward (direct services) listens. AAAA queries get NODATA:
// softnet's ingress listeners bind v4 loopback only.
type DNSServer struct {
	server *dns.Server
}

// NewDNSServer builds a server bound to the address returned by
// DNSAddr() — the default 127.0.0.1:51153 unless $DEVM_DNS_ADDR
// overrides.
func NewDNSServer() *DNSServer {
	return newDNSServerAt(DNSAddr())
}

// newDNSServerAt is the testable inner — tests pass an ephemeral
// address.
func newDNSServerAt(addr string) *DNSServer {
	mux := dns.NewServeMux()
	s := &DNSServer{
		server: &dns.Server{
			Addr:    addr,
			Net:     "udp",
			Handler: mux,
		},
	}
	mux.HandleFunc(testTLD, s.handleTest)
	return s
}

// Serve binds and serves until ctx is cancelled. Returns nil on
// graceful shutdown; returns a bind error if the port is taken.
func (s *DNSServer) Serve(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() { errCh <- s.server.ListenAndServe() }()

	debuglog.Logf("serviceapi", "dns server listening on %s", s.server.Addr)

	select {
	case <-ctx.Done():
		_ = s.server.Shutdown()
		// Drain the goroutine; it returns nil on graceful shutdown.
		<-errCh
		return nil
	case err := <-errCh:
		return err
	}
}

func (s *DNSServer) handleTest(w dns.ResponseWriter, r *dns.Msg) {
	msg := new(dns.Msg)
	msg.SetReply(r)
	msg.Authoritative = true
	for _, q := range r.Question {
		// Ingress flows entirely through softnet's host-side listeners
		// (see computeExposeMap/pushExposeMap) — every .test name,
		// direct or proxied, answers host loopback.
		switch q.Qtype {
		case dns.TypeA:
			msg.Answer = append(msg.Answer, &dns.A{
				Hdr: dns.RR_Header{Name: q.Name, Rrtype: q.Qtype, Class: dns.ClassINET, Ttl: 0},
				A:   net.IPv4(127, 0, 0, 1),
			})
		}
		// All other query types fall through; Answer stays empty —
		// the client gets NOERROR + NODATA, which is what
		// well-behaved resolvers expect for "no record of this type".
	}
	_ = w.WriteMsg(msg)
}
