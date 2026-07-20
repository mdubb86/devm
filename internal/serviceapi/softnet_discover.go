package serviceapi

import (
	"context"

	"github.com/mdubb86/devm/internal/identity"
)

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
// softnetState.put is synchronous (an in-memory map write — fast), but the
// setPolicy re-push dials a unix socket per project and can block for
// seconds against a dead/unresponsive softnet child. RunService calls this
// on the daemon's startup path, so that push runs in a goroutine — a slow
// or dead socket for one project must not stall the whole daemon (and every
// other project's `devm shell`) from coming up.
//
// ctx is accepted for parity with AdoptIronProxies and future-proofing
// (a context-aware dial); the current softnetClient doesn't use it.
//
// Alongside the ENFORCED re-push, this also best-effort re-pushes the
// ingress expose map from each project's persisted config — belt and
// suspenders: softnet's child process retains its last-applied expose
// map across a daemon-only restart (it isn't reset like the egress
// policy can be), so this mainly rehydrates the daemon's in-memory view
// and the port-claims registry pushExposeMap reconciles as a side
// effect (see expose.go).
func discoverSoftnet(ctx context.Context, cfg identity.Config, ntpPort int) {
	for _, id := range ironProxyState.keys() {
		sock := SoftnetControlSock(cfg, id)
		softnetState.put(id, sock)

		info, ok := ironProxyState.get(id)
		if !ok {
			continue
		}
		go func(id, sock string, info projectInfo) {
			_ = newSoftnetClient(sock).setPolicy("ENFORCED", endpointFrom(info, ntpPort))
			if snap, err := ReadStateSnapshot(cfg, id); err == nil && snap != nil {
				_ = pushExposeMap(id, computeExposeMap(snap.Cfg, info.ProjectIP))
			}
		}(id, sock, info)
	}
}
