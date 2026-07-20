package serviceapi

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mdubb86/devm/internal/identity"
)

func TestAllocateProjectIP_LowestFree_FromEmpty(t *testing.T) {
	resetIronProxyState(t)
	ip, err := AllocateProjectIP(identity.Prod, "myapp")
	require.NoError(t, err)
	assert.Equal(t, "127.42.0.1", ip)
}

func TestAllocateProjectIP_Idempotent(t *testing.T) {
	resetIronProxyState(t)
	first, err := AllocateProjectIP(identity.Prod, "myapp")
	require.NoError(t, err)
	second, err := AllocateProjectIP(identity.Prod, "myapp")
	require.NoError(t, err)
	assert.Equal(t, first, second, "idempotent — same projectID returns same IP")
}

func TestAllocateProjectIP_SkipsAssigned(t *testing.T) {
	resetIronProxyState(t)
	_, err := AllocateProjectIP(identity.Prod, "a")
	require.NoError(t, err)
	ipB, err := AllocateProjectIP(identity.Prod, "b")
	require.NoError(t, err)
	assert.Equal(t, "127.42.0.2", ipB)
}

func TestAllocateProjectIP_PoolExhaustion(t *testing.T) {
	resetIronProxyState(t)
	for i := 1; i <= 20; i++ {
		_, err := AllocateProjectIP(identity.Prod, itoaSA(i))
		require.NoError(t, err)
	}
	_, err := AllocateProjectIP(identity.Prod, "overflow")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pool exhausted")
}

func TestReleaseProjectIP_FreesSlot(t *testing.T) {
	resetIronProxyState(t)
	_, err := AllocateProjectIP(identity.Prod, "a")
	require.NoError(t, err)
	_, err = AllocateProjectIP(identity.Prod, "b")
	require.NoError(t, err)
	ReleaseProjectIP(identity.Prod, "a")
	ipC, err := AllocateProjectIP(identity.Prod, "c")
	require.NoError(t, err)
	assert.Equal(t, "127.42.0.1", ipC, "c should reuse a's freed slot")
}

func TestReleaseProjectIP_Idempotent(t *testing.T) {
	resetIronProxyState(t)
	ReleaseProjectIP(identity.Prod, "nonexistent") // must not panic
	_, err := AllocateProjectIP(identity.Prod, "a")
	require.NoError(t, err)
	ReleaseProjectIP(identity.Prod, "a")
	ReleaseProjectIP(identity.Prod, "a")
}

// TestAllocateProjectIP_ConcurrentDistinct guards against the TOCTOU
// race across AllocateProjectIP's three separate lock acquisitions on
// ironProxyState (get, keys+get loop, put): without allocMu serializing
// the read-decide-write critical section, concurrent /vm/start calls
// for different projects could compute and write the same lowest-free
// IP. Run with -race to also catch any unsynchronized access.
func TestAllocateProjectIP_ConcurrentDistinct(t *testing.T) {
	resetIronProxyState(t)
	const N = 10
	ips := make(chan string, N)
	errs := make(chan error, N)
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			ip, err := AllocateProjectIP(identity.Prod, id)
			if err != nil {
				errs <- err
				return
			}
			ips <- ip
		}(fmt.Sprintf("proj-%d", i))
	}
	wg.Wait()
	close(ips)
	close(errs)
	for e := range errs {
		t.Errorf("unexpected alloc error: %v", e)
	}
	seen := map[string]bool{}
	for ip := range ips {
		if seen[ip] {
			t.Errorf("duplicate IP allocated: %s", ip)
		}
		seen[ip] = true
	}
	if len(seen) != N {
		t.Errorf("expected %d distinct IPs, got %d", N, len(seen))
	}
}

// resetIronProxyState clears the package-level ironProxyState between
// tests so allocator tests don't leak state into each other.
func resetIronProxyState(t *testing.T) {
	t.Helper()
	ironProxyState = newIronProxyStore()
}

// itoaSA is a local zero-alloc int→string for the exhaustion test.
func itoaSA(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
