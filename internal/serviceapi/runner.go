package serviceapi

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/oklog/run"

	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/serviceapi/sockact"
	"github.com/mdubb86/devm/internal/supervisor"
)

// RunService composes the service's goroutines into an oklog/run
// group and blocks until any actor returns. Ship 1 only ran the
// HTTP server; Ship 2 added DNS; Ship 3 adds the reverse proxy on
// launchd-inherited :80 and :443.
//
// build is the daemon's build identity, reported via /version.
// ctx is the shutdown signal: cancel it and every actor stops.
func RunService(ctx context.Context, build Build) error {
	if _, err := EnsureRuntimeDir(); err != nil {
		return fmt.Errorf("ensure runtime dir: %w", err)
	}

	// CA — generates the root on first launch, persists, reloads later.
	ca, err := LoadOrGenerate()
	if err != nil {
		return fmt.Errorf("ca: %w", err)
	}

	// Routes table — empty on startup; CLI populates via admin API.
	routes := NewRoutes()

	// HTTP API server (Ship 1) with the /routes/* admin endpoints
	// registered on top.
	server := NewServer(SocketPath(), build)
	RegisterRoutesHandlers(server, routes)

	// VM lifecycle endpoints (Ship 4). Supervisor and tart wrapper are
	// daemon-scoped singletons; the supervisor manages the per-project VM
	// processes and survives across CLI invocations.
	tr := tart.New()
	sup := supervisor.New("")
	// Per-project mutex for every state-mutating VM endpoint (start,
	// stop, teardown, apply-egress-enforcement, reconcile). Serializes
	// concurrent same-project calls inside the daemon instead of relying
	// on the CLI-side flock.
	locks := NewProjectLocks()
	// Adopt iron-proxy processes left running by a prior daemon
	// instance. They survive daemon death by design (setsid on
	// spawn); re-attaching here means /vm/stop and /vm/status
	// behave correctly post-restart instead of orphaning them.
	// Best-effort — a failure (e.g., `ps` missing) shouldn't
	// block daemon startup.
	if err := AdoptIronProxies(ctx, sup); err != nil {
		fmt.Fprintf(os.Stderr, "iron-proxy adopt: %v\n", err)
	}
	// Denials tracker — per-project counts of iron-proxy allow-list
	// rejects, fed by the supervisor's log tap on iron-proxy stderr.
	// Adopted iron-proxies from a prior daemon instance don't get tapped
	// (we only have their PID, not their output stream), so counts
	// start empty for them until the next SpawnIronProxy respawn.
	denials := NewDenials()

	// No-secret startup heal: respawn any iron-proxy that's MISSING for
	// a project whose VM is already running. Covers the gap
	// AdoptIronProxies doesn't — a VM that outlived a killed/crashed
	// iron-proxy process, not just a killed daemon. Secret-bearing
	// projects can't be healed here (no keychain access outside a
	// user-context CLI invocation) and wait for the CLI to notice via
	// /handshake. Best-effort — logs per project, never blocks startup.
	healNoSecretProxiesAtStartup(ctx, tr, sup, denials)

	// SNTP responder — one daemon-wide instance, bound eagerly so /vm/start
	// knows the port when it builds the guest's nftables script. The guest
	// DNATs its outbound UDP:123 (timesyncd → wherever) to MAC_HOST at
	// this port; we answer from the host's wall clock. This is what heals
	// guest-clock drift after a Mac sleep — external NTP isn't reachable
	// because our egress firewall doesn't proxy UDP, but the Mac itself
	// is always time-correct.
	ntp, err := NewNTPServer()
	if err != nil {
		return fmt.Errorf("start ntp responder: %w", err)
	}

	RegisterVMHandlers(server, sup, tr, denials, ntp.Port(), locks)
	RegisterReconcileHandler(server, locks, &realApplyLiver{tr: tr}, tr, sup)
	RegisterApplyIronProxyHandler(server, locks, sup, denials)
	RegisterHandshakeHandler(server, build, sup)

	// Pull launchd-inherited listeners for :80 and :443. If the
	// daemon was started outside launchd (e.g., `devm serve` from a
	// shell), these come back as ErrNotActivated — we skip the
	// proxy actor entirely, but the rest of the daemon still works.
	httpListeners, err := sockact.Activate("HTTPSocket")
	if err != nil && !errors.Is(err, sockact.ErrNotActivated) {
		return fmt.Errorf("sockact HTTPSocket: %w", err)
	}
	httpsListeners, err := sockact.Activate("HTTPSSocket")
	if err != nil && !errors.Is(err, sockact.ErrNotActivated) {
		return fmt.Errorf("sockact HTTPSSocket: %w", err)
	}
	var g run.Group

	// HTTP API server actor (Ship 1).
	{
		serverCtx, cancel := context.WithCancel(ctx)
		g.Add(func() error {
			return server.Serve(serverCtx)
		}, func(error) {
			cancel()
		})
	}

	// DNS server actor (Ship 2).
	{
		dnsCtx, cancel := context.WithCancel(ctx)
		dnsServer := NewDNSServer()
		g.Add(func() error {
			return dnsServer.Serve(dnsCtx)
		}, func(error) {
			cancel()
		})
	}

	// SNTP responder actor. The listener is already bound in NewNTPServer
	// above so /vm/start could read the port immediately; here we start
	// the read loop.
	{
		ntpCtx, cancel := context.WithCancel(ctx)
		g.Add(func() error {
			return ntp.Serve(ntpCtx)
		}, func(error) {
			cancel()
		})
	}

	// Reverse proxy actor (Ship 3). Skipped if no listeners were
	// inherited (e.g., `devm serve` from a shell — dev convenience).
	// SetProxyReady lets `devm status`'s /proxy-status probe report
	// the actor's state instead of TCP-dialing 127.0.0.1:443 (which
	// closes mid-handshake and spams "TLS handshake error … EOF" in
	// the daemon log — the very bug this feedback loop caught).
	if len(httpListeners)+len(httpsListeners) > 0 {
		proxyCtx, cancel := context.WithCancel(ctx)
		proxy := NewProxyServer(routes, ca)
		server.SetProxyReady(true)
		g.Add(func() error {
			return proxy.Serve(proxyCtx, httpListeners, httpsListeners)
		}, func(error) {
			cancel()
		})
	}

	// Context-cancel actor: when ctx is cancelled (parent signal),
	// the group returns.
	{
		ctxCancel := make(chan struct{})
		g.Add(func() error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ctxCancel:
				return nil
			}
		}, func(error) {
			close(ctxCancel)
		})
	}

	return g.Run()
}
