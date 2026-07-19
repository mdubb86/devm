package serviceapi

import (
	"os"

	"github.com/mdubb86/devm/internal/portbinder"
)

// helperAvailable records whether the portbinder root helper (the
// `devm-portbinder` LaunchDaemon + its lo0-alias-provisioned 127.42/16
// pool) was present at daemon startup. Package-level and set once by
// RunService via detectHelperAvailable — the daemon doesn't expect the
// helper to come and go mid-run: a machine either has `devm install`'d
// it or it hasn't.
//
// Defaults to true (the production assumption) so unit tests that
// construct daemon state directly — never going through RunService —
// keep exercising the real per-project-IP pool allocator unless they
// explicitly opt into fallback mode.
//
// When false, the B3 per-project-bind-isolation paths fall back to
// pre-B3 behavior: AllocateProjectIP hands out a fixed 127.0.0.1
// instead of a pool address, SSH gets a picked ephemeral host port
// instead of binding guest :22 on a dedicated loopback alias, and
// softnet's low-port ingress binds directly instead of routing through
// the helper. This is what lets the pre-existing e2e suite keep
// passing under E2E_ISOLATE=1 (sandbox daemon, no sudo, no helper)
// without requiring `devm install` first.
var helperAvailable = true

// detectHelperAvailable reports whether the daemon should use the real
// per-project-IP path. DEVM_FORCE_FALLBACK=1 is a test-only override
// for asserting fallback behavior on a machine where the helper
// happens to be installed; absent that, it's purely a stat of
// portbinder.SocketPath.
func detectHelperAvailable() bool {
	if os.Getenv("DEVM_FORCE_FALLBACK") == "1" {
		return false
	}
	if _, err := os.Stat(portbinder.SocketPath); err != nil {
		return false
	}
	return true
}
