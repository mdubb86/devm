package serviceapi

import (
	"context"
	"fmt"
	"net"
)

// dnsResolver is the small surface of net.Resolver that
// CheckDNSHealth uses. Lets tests swap in a fake.
type dnsResolver interface {
	LookupIP(ctx context.Context, network, host string) ([]net.IP, error)
}

// dnsProbeName is the synthetic name we resolve to verify that
// *.test resolution actually works through the system. The .test
// TLD is reserved (RFC 6761) so this name is safe to use without
// risk of accidental collision.
const dnsProbeName = "devm-health-check.test"

// CheckDNSHealth verifies that *.test queries return 127.0.0.1 via
// the system resolver. Returns nil on success.
//
// Distinct from CheckResolverFile: the file can exist while the
// daemon is down (port closed) or pointed at a different port.
// Resolving an actual name catches both cases plus any other
// breakage in the resolver chain.
func CheckDNSHealth(ctx context.Context) error {
	return checkDNSHealthWith(ctx, net.DefaultResolver)
}

func checkDNSHealthWith(ctx context.Context, r dnsResolver) error {
	ips, err := r.LookupIP(ctx, "ip4", dnsProbeName)
	if err != nil {
		return fmt.Errorf("resolving %s: %w", dnsProbeName, err)
	}
	for _, ip := range ips {
		if ip.Equal(net.IPv4(127, 0, 0, 1)) {
			return nil
		}
	}
	return fmt.Errorf("expected %s to resolve to 127.0.0.1, got %v", dnsProbeName, ips)
}
