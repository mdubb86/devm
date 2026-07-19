package serviceapi

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAllocateProjectIP_LowestFree_FromEmpty(t *testing.T) {
	resetIronProxyState(t)
	ip, err := AllocateProjectIP("myapp")
	require.NoError(t, err)
	assert.Equal(t, "127.42.0.1", ip)
}

func TestAllocateProjectIP_Idempotent(t *testing.T) {
	resetIronProxyState(t)
	first, err := AllocateProjectIP("myapp")
	require.NoError(t, err)
	second, err := AllocateProjectIP("myapp")
	require.NoError(t, err)
	assert.Equal(t, first, second, "idempotent — same projectID returns same IP")
}

func TestAllocateProjectIP_SkipsAssigned(t *testing.T) {
	resetIronProxyState(t)
	_, err := AllocateProjectIP("a")
	require.NoError(t, err)
	ipB, err := AllocateProjectIP("b")
	require.NoError(t, err)
	assert.Equal(t, "127.42.0.2", ipB)
}

func TestAllocateProjectIP_PoolExhaustion(t *testing.T) {
	resetIronProxyState(t)
	for i := 1; i <= 20; i++ {
		_, err := AllocateProjectIP(itoaSA(i))
		require.NoError(t, err)
	}
	_, err := AllocateProjectIP("overflow")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pool exhausted")
}

func TestReleaseProjectIP_FreesSlot(t *testing.T) {
	resetIronProxyState(t)
	_, err := AllocateProjectIP("a")
	require.NoError(t, err)
	_, err = AllocateProjectIP("b")
	require.NoError(t, err)
	ReleaseProjectIP("a")
	ipC, err := AllocateProjectIP("c")
	require.NoError(t, err)
	assert.Equal(t, "127.42.0.1", ipC, "c should reuse a's freed slot")
}

func TestReleaseProjectIP_Idempotent(t *testing.T) {
	resetIronProxyState(t)
	ReleaseProjectIP("nonexistent") // must not panic
	_, err := AllocateProjectIP("a")
	require.NoError(t, err)
	ReleaseProjectIP("a")
	ReleaseProjectIP("a")
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
			ip, err := AllocateProjectIP(id)
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

// TestAllocateProjectIP_ReverseDirection pins the B3 standalone-tests
// e2e lane's collision-avoidance trick: DEVM_PROJECT_IP_ALLOC_DIRECTION
// =reverse walks the pool from the top (127.42.0.20) down instead of
// from the bottom, so a sandbox daemon sharing the real portbinder
// helper with a production daemon on the same machine claims slots
// from the opposite end of the pool.
func TestAllocateProjectIP_ReverseDirection(t *testing.T) {
	t.Setenv("DEVM_PROJECT_IP_ALLOC_DIRECTION", "reverse")
	resetIronProxyState(t)
	ip, err := AllocateProjectIP("myproj")
	require.NoError(t, err)
	assert.Equal(t, "127.42.0.20", ip)
}

// TestAllocateProjectIP_ReverseDirection_SkipsAssigned pairs with the
// forward-direction TestAllocateProjectIP_SkipsAssigned: the second
// project should get the pool's second-from-the-top slot.
func TestAllocateProjectIP_ReverseDirection_SkipsAssigned(t *testing.T) {
	t.Setenv("DEVM_PROJECT_IP_ALLOC_DIRECTION", "reverse")
	resetIronProxyState(t)
	_, err := AllocateProjectIP("a")
	require.NoError(t, err)
	ipB, err := AllocateProjectIP("b")
	require.NoError(t, err)
	assert.Equal(t, "127.42.0.19", ipB)
}

// TestAllocateProjectIP_Fallback pins Change 1's core primitive: when
// the portbinder helper isn't available, every project gets the fixed
// fallbackProjectIP regardless of the pool or allocation direction —
// this is the pre-B3 behavior the isolated (no-sudo, no-helper) e2e
// lane depends on.
func TestAllocateProjectIP_Fallback(t *testing.T) {
	resetIronProxyState(t)
	old := helperAvailable
	helperAvailable = false
	t.Cleanup(func() { helperAvailable = old })

	ipA, err := AllocateProjectIP("a")
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1", ipA)

	// Idempotent, and every OTHER project gets the same fixed address
	// too — fallback mode has no pool to exhaust.
	ipB, err := AllocateProjectIP("b")
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1", ipB)

	ipAAgain, err := AllocateProjectIP("a")
	require.NoError(t, err)
	assert.Equal(t, ipA, ipAAgain)
}

// TestAllocateSSHPort_Fallback pins AllocateSSHPort's two modes: a
// no-op (always 0) when the helper is available, and an idempotent
// picked ephemeral port when it isn't.
func TestAllocateSSHPort_Fallback(t *testing.T) {
	resetIronProxyState(t)
	old := helperAvailable
	t.Cleanup(func() { helperAvailable = old })

	helperAvailable = true
	port, err := AllocateSSHPort("proj-real")
	require.NoError(t, err)
	assert.Equal(t, 0, port, "helper available: no host port to pick")

	helperAvailable = false
	first, err := AllocateSSHPort("proj-fallback")
	require.NoError(t, err)
	assert.NotZero(t, first, "fallback mode: must pick a real port")

	second, err := AllocateSSHPort("proj-fallback")
	require.NoError(t, err)
	assert.Equal(t, first, second, "idempotent — same projectID keeps the same picked port")
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
