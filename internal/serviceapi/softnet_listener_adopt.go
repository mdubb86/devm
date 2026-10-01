package serviceapi

import (
	"context"
	"fmt"
	"net"

	"github.com/mdubb86/devm/internal/daemonlog"
	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/sandbox/tart"
)

// bindSoftnetListenersForAdopt re-binds a project's propose HTTP
// listener after a daemon restart and pushes softnet's forward-target
// map to point at the new local port. On daemon restart, the softnet
// child processes survive alongside their VMs but retain their last
// setPolicy from the OLD daemon — so their propose:82 forward still
// points at a TCP port the old daemon's listener was bound to, which
// is now dead. Without this rebind, gdevm propose / passthrough /
// upgrade / recipes all get connection-refused until the user runs
// `devm stop && devm start` on each project.
//
// The listener-binding logic mirrors the /vm/start handler's
// proposeLn setup exactly: same registration-before-goroutine ordering
// so a fast /vm/stop can't leak an fd; same handler wiring.
//
// Updates ironProxyState with the fresh ProposePort, then pushes
// setPolicy("FORWARDING", endpointFrom(...)) so softnet routes
// 192.168.127.1:82 to this daemon's new port.
//
// Best-effort — a failure on one project logs and doesn't block the
// startup rehydrate loop for other projects. If binding fails the
// project's guest-initiated softnet paths stay broken until the user
// runs `devm stop && devm start` on it, but the daemon can still serve
// the Mac-side API for that project.
func bindSoftnetListenersForAdopt(
	ctx context.Context,
	cfg identity.Config,
	cache *StateCache,
	tr *tart.Tart,
	locks *ProjectLocks,
	projectName string,
	ntpPort int,
) error {
	popPort, err := pickPort()
	if err != nil {
		return fmt.Errorf("pick pop port: %w", err)
	}
	popLn, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", popPort))
	if err != nil {
		return fmt.Errorf("bind pop listener: %w", err)
	}
	popListeners.Store(projectName, popLn)
	go servePopListener(popLn, cfg, projectName)

	proposePort, err := pickPort()
	if err != nil {
		return fmt.Errorf("pick propose port: %w", err)
	}
	proposeLn, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", proposePort))
	if err != nil {
		return fmt.Errorf("bind propose listener: %w", err)
	}
	proposeListeners.Store(projectName, proposeLn)
	go serveProposeListener(proposeLn, cfg, cache, tr, locks, projectName)

	// Update ironProxyState with the fresh ports so endpointFrom picks
	// them up when we push softnet's forwarding table below.
	info, _ := ironProxyState.get(projectName)
	info.PopPort = popPort
	info.ProposePort = proposePort
	ironProxyState.put(projectName, info)

	// Push softnet's forward-target map. On daemon restart softnet still
	// holds the OLD daemon's Propose port; without this push, the
	// guest-initiated paths remain dead even though our listeners are
	// bound and ready. Async — softnetClient.dial can retry for ~11s
	// against a slow/unresponsive softnet child, and RunService can't
	// block startup on any one project (see softnet_discover.go's
	// discoverSoftnet, which fires the equivalent push in a goroutine
	// for exactly this reason). Errors are logged inline.
	sock := SoftnetControlSock(cfg, projectName)
	ep := endpointFrom(info, ntpPort)
	go func() {
		if err := newSoftnetClient(sock).setPolicy("FORWARDING", ep); err != nil {
			daemonlog.Errorf("serviceapi: adopt-rebind softnet setPolicy for %s: %v", projectName, err)
		}
	}()
	return nil
}
