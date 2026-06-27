package serviceapi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/oklog/run"

	"github.com/mdubb86/devm/internal/debuglog"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/serviceapi/sockact"
	"github.com/mdubb86/devm/internal/supervisor"
)

// filterBoundListeners drops listeners that launchd handed back unbound.
//
// When a LaunchAgent declares Sockets for privileged ports (:80, :443),
// launchd returns file descriptors but does not actually bind them —
// a user-level launchd context can't bind <1024. The fds surface as
// listeners with Addr "0.0.0.0:0", and Accept on them returns EINVAL,
// crashing the proxy actor and (under KeepAlive) the whole daemon in
// a respawn loop.
//
// Resolution requires splitting the daemon: a LaunchDaemon (root) that
// binds :80/:443 and forwards fds via Unix-socket SCM_RIGHTS to the
// user-level LaunchAgent that does everything else. Tracked at
// docs/superpowers/specs/2026-06-27-launchagent-vs-launchdaemon.md.
// Until that lands, the daemon runs in proxy-disabled mode.
func filterBoundListeners(in []net.Listener) []net.Listener {
	out := in[:0]
	for _, ln := range in {
		addr := ln.Addr().String()
		if strings.HasSuffix(addr, ":0") {
			debuglog.Logf("serviceapi",
				"proxy: dropping unbound launchd listener %s — privileged port socket activation requires LaunchDaemon", addr)
			_ = ln.Close()
			continue
		}
		out = append(out, ln)
	}
	return out
}

// RunService composes the service's goroutines into an oklog/run
// group and blocks until any actor returns. Ship 1 only ran the
// HTTP server; Ship 2 added DNS; Ship 3 adds the reverse proxy on
// launchd-inherited :80 and :443.
//
// version is the build version reported by /version. ctx is the
// shutdown signal: cancel it and every actor stops.
func RunService(ctx context.Context, version string) error {
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
	server := NewServer(SocketPath(), version)
	RegisterRoutesHandlers(server, routes)

	// VM lifecycle endpoints (Ship 4). Supervisor and tart wrapper are
	// daemon-scoped singletons; the supervisor manages the per-project VM
	// processes and survives across CLI invocations.
	tr := tart.New()
	sup := supervisor.New("")
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
	// Discard listeners that launchd handed back unbound (Addr "0.0.0.0:0").
	// A user-level LaunchAgent can't bind privileged ports through
	// launchd's socket activation in all configurations — when it
	// can't, launchd still returns fds but Accept on them surfaces
	// EINVAL. Keep the daemon up in degraded mode; revisit the
	// LaunchAgent → LaunchDaemon split as a separate ship.
	httpListeners = filterBoundListeners(httpListeners)
	httpsListeners = filterBoundListeners(httpsListeners)

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
