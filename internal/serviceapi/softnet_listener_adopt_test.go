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
// the adopt-path listener rehydrate actually binds a new propose
// listener and updates ironProxyState with the fresh port. The softnet
// setPolicy push at the end will fail (no real softnet socket in the test
// environment), but the bind should complete before that — proving that
// a daemon restart re-establishes the guest-facing TCP endpoint even if
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
		if v, ok := guestAPIListeners.LoadAndDelete("proj"); ok {
			_ = v.(interface{ Close() error }).Close()
		}
	})

	err := bindSoftnetListenersForAdopt(
		context.Background(), cfg, cache, tr, locks,
		"proj", 12345,
	)
	// bindSoftnetListenersForAdopt returns nil on success — the softnet
	// setPolicy push happens asynchronously so a slow softnet child
	// can't stall daemon startup on any one project. Verify the
	// synchronous parts succeeded:
	//   1. guestAPIListeners has an entry for "proj"
	//   2. ironProxyState is updated with a non-zero GuestAPIPort
	// The async setPolicy in a real environment logs on failure; the
	// unit test doesn't wait on it.
	require.NoError(t, err, "listener bind should succeed even without a softnet control socket (setPolicy is async)")

	_, bound := guestAPIListeners.Load("proj")
	assert.True(t, bound, "guest-API listener must be bound even if softnet push fails")

	info, ok := ironProxyState.get("proj")
	require.True(t, ok)
	assert.NotZero(t, info.GuestAPIPort, "GuestAPIPort must be updated with the fresh listener port")
}
