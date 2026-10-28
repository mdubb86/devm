package serviceapi

import (
	"errors"
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

// TestAllocateProjectIP_SkipsSquattedIP pins the orphaned-VM defense:
// an IP whose :22 answers a connect probe is held by a process outside
// daemon state (e.g. a VM whose state snapshot was lost) and must be
// skipped, or DNS for the new project would point at the orphan's sshd.
func TestAllocateProjectIP_SkipsSquattedIP(t *testing.T) {
	resetIronProxyState(t)
	probeIPInUse = func(ip string) bool { return ip == "127.42.0.1" || ip == "127.42.0.2" }
	ip, err := AllocateProjectIP(identity.Prod, "a")
	require.NoError(t, err)
	assert.Equal(t, "127.42.0.3", ip, "squatted .1/.2 must be skipped")
}

func TestAllocateProjectIP_AllSquattedExhaustsPool(t *testing.T) {
	resetIronProxyState(t)
	probeIPInUse = func(string) bool { return true }
	_, err := AllocateProjectIP(identity.Prod, "a")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pool exhausted")
	assert.Contains(t, err.Error(), "127.42.0.1", "error must enumerate the blocked slots")
	assert.Contains(t, err.Error(), "specific LISTEN on :22", "error must name the cause")
}

// TestAllocateProjectIP_ExhaustionErrorNamesMixedCauses pins the
// shape of the exhaustion error: when some slots are held by tracked
// projects and others by foreign specific listeners, the error must
// name both causes with the specific IPs — otherwise an operator
// cannot tell whether to free a project or a listener.
func TestAllocateProjectIP_ExhaustionErrorNamesMixedCauses(t *testing.T) {
	resetIronProxyState(t)
	// Fill 127.42.0.1..3 via tracked projects.
	_, err := AllocateProjectIP(identity.Prod, "famfolio")
	require.NoError(t, err)
	_, err = AllocateProjectIP(identity.Prod, "everstone")
	require.NoError(t, err)
	_, err = AllocateProjectIP(identity.Prod, "ffbot")
	require.NoError(t, err)
	// Block the rest of the pool via the probe.
	probeIPInUse = func(string) bool { return true }
	_, err = AllocateProjectIP(identity.Prod, "newproj")
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "127.42.0.1=famfolio")
	assert.Contains(t, msg, "127.42.0.2=everstone")
	assert.Contains(t, msg, "127.42.0.3=ffbot")
	assert.Contains(t, msg, "127.42.0.4")
	assert.Contains(t, msg, "127.42.0.20")
	assert.Contains(t, msg, "specific LISTEN on :22")
	assert.Contains(t, msg, "Remote Login", "error must explicitly disclaim the wildcard *:22 case")
}

// TestAllocateProjectIP_ExistingAssignmentNotProbed pins that the
// idempotent path returns the recorded IP without probing: once the
// project's own softnet is up, its own :22 listener answers the probe,
// and re-allocation must not mistake the project for a squatter.
func TestAllocateProjectIP_ExistingAssignmentNotProbed(t *testing.T) {
	resetIronProxyState(t)
	first, err := AllocateProjectIP(identity.Prod, "a")
	require.NoError(t, err)
	probeIPInUse = func(string) bool { return true }
	second, err := AllocateProjectIP(identity.Prod, "a")
	require.NoError(t, err)
	assert.Equal(t, first, second)
}

// TestProbeIPInUse_SpecificBindBusy pins that a specific-address
// LISTEN on ip:22 marks the slot occupied.
func TestProbeIPInUse_SpecificBindBusy(t *testing.T) {
	old := listenersBoundTo
	t.Cleanup(func() { listenersBoundTo = old })
	listenersBoundTo = func(ip string) ([]int, error) {
		if ip == "127.42.0.5" {
			return []int{22, 80, 443}, nil
		}
		return nil, nil
	}
	assert.True(t, probeIPInUse("127.42.0.5"))
	assert.False(t, probeIPInUse("127.42.0.6"))
}

// TestProbeIPInUse_WildcardListenerIsFree is the regression test for
// the Remote-Login-enabled scenario: host sshd on *:22 accepts on
// every lo0 alias but is NOT a specific bind, so listenersBoundTo
// returns [] and the slot must probe as free.
func TestProbeIPInUse_WildcardListenerIsFree(t *testing.T) {
	old := listenersBoundTo
	t.Cleanup(func() { listenersBoundTo = old })
	listenersBoundTo = func(string) ([]int, error) { return nil, nil }
	assert.False(t, probeIPInUse("127.42.0.5"), "wildcard listener must not count as occupied")
}

// TestProbeIPInUse_QueryErrorFailsClosed pins the fail-closed
// behavior: if listenersBoundTo errors, probeIPInUse must treat the
// slot as occupied — handing out an IP whose listener state we
// cannot verify risks cross-wiring an orphan.
func TestProbeIPInUse_QueryErrorFailsClosed(t *testing.T) {
	old := listenersBoundTo
	t.Cleanup(func() { listenersBoundTo = old })
	listenersBoundTo = func(string) ([]int, error) { return nil, errors.New("netstat broke") }
	assert.True(t, probeIPInUse("127.42.0.5"))
}

// resetIronProxyState clears the package-level ironProxyState between
// tests so allocator tests don't leak state into each other. It also
// stubs probeIPInUse to "free" — the real probe reads live machine
// state (whatever holds 127.42.0.N:22 on the dev box), which would
// make allocator tests non-hermetic.
func resetIronProxyState(t *testing.T) {
	t.Helper()
	ironProxyState = newIronProxyStore()
	old := probeIPInUse
	probeIPInUse = func(string) bool { return false }
	t.Cleanup(func() { probeIPInUse = old })
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
