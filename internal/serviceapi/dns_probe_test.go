package serviceapi

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeResolver implements dnsResolver for tests — lets us avoid
// hitting the real system resolver during unit tests.
type fakeResolver struct {
	ips []net.IP
	err error
}

func (f *fakeResolver) LookupIP(ctx context.Context, network, host string) ([]net.IP, error) {
	return f.ips, f.err
}

func TestCheckDNSHealth_OK(t *testing.T) {
	r := &fakeResolver{ips: []net.IP{net.IPv4(127, 0, 0, 1)}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := checkDNSHealthWith(ctx, r)
	require.NoError(t, err)
}

func TestCheckDNSHealth_WrongAnswer(t *testing.T) {
	r := &fakeResolver{ips: []net.IP{net.IPv4(192, 168, 1, 1)}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := checkDNSHealthWith(ctx, r)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected")
}

func TestCheckDNSHealth_ResolverError(t *testing.T) {
	r := &fakeResolver{err: &net.DNSError{Err: "no such host"}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := checkDNSHealthWith(ctx, r)
	require.Error(t, err)
}
