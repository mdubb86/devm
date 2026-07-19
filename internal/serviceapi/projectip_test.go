package serviceapi

import (
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
