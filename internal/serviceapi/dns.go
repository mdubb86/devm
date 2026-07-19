package serviceapi

import (
	"context"
	"net"
	"os"
	"strings"

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
// answers its owning project's allocated ProjectIP (A), TTL 0 —
// isolation guarantee: a name only resolves while its project is
// running and only to that project's own 127.42.0.N address. AAAA
// queries get NODATA: softnet's ingress listeners bind v4 loopback
// only.
type DNSServer struct {
	server *dns.Server
	lookup func(project string) (string, bool)
}

// NewDNSServer builds a server bound to the address returned by
// DNSAddr() — the default 127.0.0.1:51153 unless $DEVM_DNS_ADDR
// overrides. projectIPLookup returns (projectIP, true) for a known,
// running project name; (_, false) for unknown or stopped projects.
func NewDNSServer(projectIPLookup func(project string) (string, bool)) *DNSServer {
	return newDNSServerAt(DNSAddr(), projectIPLookup)
}

// newDNSServerAt is the testable inner — tests pass an ephemeral
// address.
func newDNSServerAt(addr string, projectIPLookup func(string) (string, bool)) *DNSServer {
	mux := dns.NewServeMux()
	s := &DNSServer{
		server: &dns.Server{
			Addr:    addr,
			Net:     "udp",
			Handler: mux,
		},
		lookup: projectIPLookup,
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
		if q.Qtype != dns.TypeA {
			continue // AAAA and others: NODATA (Answer stays empty).
		}
		// devm-health-check.test is a reserved sentinel name (see
		// dns_probe.go's CheckDNSHealth, used by `devm status`) that
		// verifies *.test queries actually reach this resolver — it
		// isn't a project and must always answer loopback, independent
		// of the per-project lookup below.
		if strings.EqualFold(strings.TrimSuffix(q.Name, "."), strings.TrimSuffix(dnsProbeName, ".")) {
			msg.Answer = append(msg.Answer, &dns.A{
				Hdr: dns.RR_Header{Name: q.Name, Rrtype: q.Qtype, Class: dns.ClassINET, Ttl: 0},
				A:   net.IPv4(127, 0, 0, 1),
			})
			continue
		}
		project := extractProjectLabel(q.Name)
		ip, ok := s.lookup(project)
		if !ok {
			msg.Rcode = dns.RcodeNameError // NXDOMAIN
			continue
		}
		msg.Answer = append(msg.Answer, &dns.A{
			Hdr: dns.RR_Header{Name: q.Name, Rrtype: q.Qtype, Class: dns.ClassINET, Ttl: 0},
			A:   net.ParseIP(ip).To4(),
		})
	}
	_ = w.WriteMsg(msg)
}

// extractProjectLabel returns the project name from a *.test query.
// "myapp.test."         → "myapp"
// "api.myapp.test."     → "myapp"
// "foo.bar.myapp.test." → "myapp"
// Empty on malformed input; caller treats empty as unknown.
func extractProjectLabel(qname string) string {
	name := strings.ToLower(strings.TrimSuffix(qname, "."))
	name = strings.TrimSuffix(name, ".test")
	if name == "" {
		return ""
	}
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i+1:]
	}
	return name
}
