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
	// Adopt iron-proxy processes left running by a prior daemon
	// instance. They survive daemon death by design (setsid on
	// spawn); re-attaching here means /vm/stop and /vm/status
	// behave correctly post-restart instead of orphaning them.
	// Best-effort — a failure (e.g., `ps` missing) shouldn't
	// block daemon startup.
	if err := AdoptIronProxies(ctx, sup); err != nil {
		fmt.Fprintf(os.Stderr, "iron-proxy adopt: %v\n", err)
	}
	RegisterVMHandlers(server, sup, tr)

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

	// Reverse proxy actor (Ship 3). Skipped if no listeners were
	// inherited (e.g., `devm serve` from a shell — dev convenience).
	if len(httpListeners)+len(httpsListeners) > 0 {
		proxyCtx, cancel := context.WithCancel(ctx)
		proxy := NewProxyServer(routes, ca)
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
