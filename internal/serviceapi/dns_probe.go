package serviceapi

import (
	"context"
	"fmt"
	"net"

	"github.com/mdubb86/devm/internal/identity"
)

// dnsResolver is the small surface of net.Resolver that
// CheckDNSHealth uses. Lets tests swap in a fake.
type dnsResolver interface {
	LookupIP(ctx context.Context, network, host string) ([]net.IP, error)
}

// dnsProbeName is the synthetic name we resolve to verify that
// *.<TLD> resolution actually works through the system. The .test /
// .e2e.test TLDs are reserved (RFC 6761's .test) so this name is safe
// to use without risk of accidental collision.
func dnsProbeName(cfg identity.Config) string {
	return "devm-health-check." + cfg.TLD
}

// CheckDNSHealth verifies that *.<TLD> queries return 127.0.0.1 via
// the system resolver. Returns nil on success.
//
// Distinct from CheckResolverFile: the file can exist while the
// daemon is down (port closed) or pointed at a different port.
// Resolving an actual name catches both cases plus any other
// breakage in the resolver chain.
func CheckDNSHealth(ctx context.Context, cfg identity.Config) error {
	return checkDNSHealthWith(ctx, cfg, net.DefaultResolver)
}

func checkDNSHealthWith(ctx context.Context, cfg identity.Config, r dnsResolver) error {
	name := dnsProbeName(cfg)
	ips, err := r.LookupIP(ctx, "ip4", name)
	if err != nil {
		return fmt.Errorf("resolving %s: %w", name, err)
	}
	for _, ip := range ips {
		if ip.Equal(net.IPv4(127, 0, 0, 1)) {
			return nil
		}
	}
	return fmt.Errorf("expected %s to resolve to 127.0.0.1, got %v", name, ips)
}
