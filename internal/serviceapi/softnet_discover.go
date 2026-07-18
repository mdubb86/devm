package serviceapi

import "context"

// discoverSoftnet rehydrates softnetState after a daemon restart.
// `tart run --net-softnet` setsid's the VM process (see
// internal/supervisor/setsid_darwin.go), and softnet runs as its child,
// so both survive daemon death exactly like iron-proxy does. But
// softnetState is in-memory only — after a restart it's empty even
// though the running VM's softnet still holds its last-applied policy.
//
// This must run after AdoptIronProxies has rehydrated ironProxyState
// (RunService hooks it immediately after, on the same startup path): for
// each recovered project it re-puts the deterministic control socket
// (SoftnetControlSock is a pure function of the project id, so no
// coordination is needed to reconstruct it) and best-effort re-pushes
// ENFORCED. The push is a reconcile, not the source of truth — softnet
// already holds its last policy — but it covers the case where softnet
// itself also restarted and came back up in its LOCKED boot default, and
// it brings the daemon's in-memory view back in sync either way.
//
// ctx is accepted for parity with AdoptIronProxies and future-proofing
// (a context-aware dial); the current softnetClient doesn't use it.
func discoverSoftnet(ctx context.Context, ntpPort int) {
	for _, id := range ironProxyState.keys() {
		sock := SoftnetControlSock(id)
		softnetState.put(id, sock)

		info, ok := ironProxyState.get(id)
		if !ok {
			continue
		}
		_ = newSoftnetClient(sock).setPolicy("ENFORCED", endpointFrom(info, ntpPort))
	}
}
