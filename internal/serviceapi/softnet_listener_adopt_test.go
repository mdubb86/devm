package serviceapi

import (
	"context"
	"testing"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBindSoftnetListenersForAdopt_BindsListenersAndUpdatesState pins that
// the adopt-path listener rehydrate actually binds new pop + propose
// listeners and updates ironProxyState with the fresh ports. The softnet
// setPolicy push at the end will fail (no real softnet socket in the test
// environment), but the bind should complete before that — proving that
// a daemon restart re-establishes the guest-facing TCP endpoints even if
// the softnet control-socket push fails independently.
func TestBindSoftnetListenersForAdopt_BindsListenersAndUpdatesState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := identity.Prod
	cache := NewStateCache()
	tr := tart.New()
	locks := NewProjectLocks()

	// Seed ironProxyState so bindSoftnetListenersForAdopt has a project
	// entry to update. The adopt path always runs after AdoptIronProxies
	// has populated this — mirror that precondition.
	ironProxyState.put("proj", projectInfo{ProjectIP: "127.42.0.42"})
	t.Cleanup(func() {
		ironProxyState.del("proj")
		if v, ok := popListeners.LoadAndDelete("proj"); ok {
			_ = v.(interface{ Close() error }).Close()
		}
		if v, ok := proposeListeners.LoadAndDelete("proj"); ok {
			_ = v.(interface{ Close() error }).Close()
		}
	})

	err := bindSoftnetListenersForAdopt(
		context.Background(), cfg, cache, tr, locks,
		"proj", nil, nil, 12345,
	)
	// setPolicy will fail (no real softnet socket), but the listener
	// bind should have already happened. The function returns an error
	// wrapping the setPolicy failure. Verify:
	//   1. popListeners has an entry for "proj"
	//   2. proposeListeners has an entry for "proj"
	//   3. ironProxyState is updated with non-zero PopPort/ProposePort
	// The error is expected and non-fatal per the caller in runner.go.
	require.Error(t, err, "expected softnet setPolicy to fail without a real socket")

	_, popBound := popListeners.Load("proj")
	assert.True(t, popBound, "pop listener must be bound even if softnet push fails")

	_, proposeBound := proposeListeners.Load("proj")
	assert.True(t, proposeBound, "propose listener must be bound even if softnet push fails")

	info, ok := ironProxyState.get("proj")
	require.True(t, ok)
	assert.NotZero(t, info.PopPort, "PopPort must be updated with the fresh listener port")
	assert.NotZero(t, info.ProposePort, "ProposePort must be updated with the fresh listener port")
	assert.NotEqual(t, info.PopPort, info.ProposePort, "pop and propose must not share a port")
}
