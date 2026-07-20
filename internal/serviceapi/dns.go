package serviceapi

import (
	"context"
	"net"
	"strings"

	"github.com/miekg/dns"

	"github.com/mdubb86/devm/internal/debuglog"
	"github.com/mdubb86/devm/internal/identity"
)

// DNSServer is the daemon's tiny *.<TLD> resolver. Every *.<TLD> name
// answers its owning project's allocated ProjectIP (A), TTL 0 —
// isolation guarantee: a name only resolves while its project is
// running and only to that project's own 127.42.0.N address. AAAA
// queries get NODATA: softnet's ingress listeners bind v4 loopback
// only.
type DNSServer struct {
	cfg    identity.Config
	server *dns.Server
	lookup func(project string) (string, bool)
}

// NewDNSServer builds a server bound to cfg.DNSBindAddr, serving
// cfg.TLD queries. projectIPLookup returns (projectIP, true) for a
// known, running project name; (_, false) for unknown or stopped
// projects.
func NewDNSServer(cfg identity.Config, projectIPLookup func(project string) (string, bool)) *DNSServer {
	return newDNSServerAt(cfg, cfg.DNSBindAddr, projectIPLookup)
}

// newDNSServerAt is the testable inner — tests pass an ephemeral
// address.
func newDNSServerAt(cfg identity.Config, addr string, projectIPLookup func(string) (string, bool)) *DNSServer {
	mux := dns.NewServeMux()
	s := &DNSServer{
		cfg: cfg,
		server: &dns.Server{
			Addr:    addr,
			Net:     "udp",
			Handler: mux,
		},
		lookup: projectIPLookup,
	}
	// miekg/dns requires the trailing dot on the TLD suffix.
	mux.HandleFunc(cfg.TLD+".", s.handleTest)
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
	probeName := dnsProbeName(s.cfg)
	for _, q := range r.Question {
		if q.Qtype != dns.TypeA {
			continue // AAAA and others: NODATA (Answer stays empty).
		}
		// probeName is a reserved sentinel name (see dns_probe.go's
		// CheckDNSHealth, used by `devm status`) that verifies *.<TLD>
		// queries actually reach this resolver — it isn't a project and
		// must always answer loopback, independent of the per-project
		// lookup below.
		if strings.EqualFold(strings.TrimSuffix(q.Name, "."), strings.TrimSuffix(probeName, ".")) {
			msg.Answer = append(msg.Answer, &dns.A{
				Hdr: dns.RR_Header{Name: q.Name, Rrtype: q.Qtype, Class: dns.ClassINET, Ttl: 0},
				A:   net.IPv4(127, 0, 0, 1),
			})
			continue
		}
		project := extractProjectLabel(q.Name, s.cfg.TLD)
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

// extractProjectLabel returns the project name from a *.<tld> query.
// "myapp.test."         → "myapp" (tld "test")
// "api.myapp.test."     → "myapp"
// "foo.bar.myapp.test." → "myapp"
// Empty on malformed input; caller treats empty as unknown. Primitive:
// takes the TLD it needs, not the whole Config.
func extractProjectLabel(qname, tld string) string {
	name := strings.ToLower(strings.TrimSuffix(qname, "."))
	name = strings.TrimSuffix(name, "."+tld)
	if name == "" {
		return ""
	}
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i+1:]
	}
	return name
}
