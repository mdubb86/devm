package serviceapi

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/oklog/run"

	"github.com/mdubb86/devm/internal/debuglog"
	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/supervisor"
)

// rebindBackoff is the wait before each retry attempt (the first
// attempt is immediate). Three attempts total, matching the
// launchdBootstrapBackoff pattern.
var rebindBackoff = []time.Duration{0, 500 * time.Millisecond, 1500 * time.Millisecond}

// helperReadinessTimeout is the max time waitForHelperReady blocks
// before proceeding. Retry in rebindProjectListeners covers slow
// helpers past this window.
const helperReadinessTimeout = 2 * time.Second

// waitForHelperReady blocks until a UDS dial to socketPath succeeds
// or timeout elapses. Best-effort; a timeout is not fatal — the
// retry loop in rebindProjectListeners covers late-arriving helper.
func waitForHelperReady(ctx context.Context, socketPath string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return
		}
		c, err := net.DialTimeout("unix", socketPath, 200*time.Millisecond)
		if err == nil {
			c.Close()
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// rebindProjectListeners calls proxy.StartProjectListeners for one
// project with bounded retry. Returns the final RebindStatus (also
// recorded on proxy). Called concurrently, one goroutine per
// recovered project.
func rebindProjectListeners(ctx context.Context, proxy *ProxyServer, cfg identity.Config, projectID, projectIP string) RebindStatus {
	proxy.RecordRebindStatus(projectID, RebindStatus{State: RebindPending, Attempts: 0})
	var lastErr error
	for i, backoff := range rebindBackoff {
		if backoff > 0 {
			select {
			case <-ctx.Done():
				s := RebindStatus{State: RebindFailed, Attempts: i, LastError: "context canceled"}
				proxy.RecordRebindStatus(projectID, s)
				return s
			case <-time.After(backoff):
			}
		}
		attempts := i + 1
		proxy.RecordRebindStatus(projectID, RebindStatus{State: RebindPending, Attempts: attempts})
		err := proxy.StartProjectListeners(ctx, projectID, projectIP)
		if err == nil {
			s := RebindStatus{State: RebindOK, Attempts: attempts}
			proxy.RecordRebindStatus(projectID, s)
			return s
		}
		lastErr = err
	}
	s := RebindStatus{State: RebindFailed, Attempts: len(rebindBackoff), LastError: lastErr.Error()}
	proxy.RecordRebindStatus(projectID, s)
	debuglog.Logf("serviceapi", "rebind: project %s FAILED after %d attempts: %v", projectID, len(rebindBackoff), lastErr)
	return s
}

// RunService composes the service's goroutines into an oklog/run
// group and blocks until any actor returns. Ship 1 only ran the
// HTTP server; Ship 2 added DNS; Ship 3 adds the reverse proxy on
// launchd-inherited :80 and :443.
//
// cfg is the daemon's compile-time identity (TLD, runtime dir, pool
// range, CA CN, base image name — prod vs. e2e). build is the
// daemon's build identity, reported via /version. ctx is the
// shutdown signal: cancel it and every actor stops.
func RunService(ctx context.Context, cfg identity.Config, build Build) error {
	if _, err := EnsureRuntimeDir(cfg); err != nil {
		return fmt.Errorf("ensure runtime dir: %w", err)
	}

	// CA — generates the root on first launch, persists, reloads later.
	ca, err := LoadOrGenerate(cfg)
	if err != nil {
		return fmt.Errorf("ca: %w", err)
	}

	// Routes table — empty on startup; CLI populates via admin API.
	routes := NewRoutes()

	// Reverse proxy (Ship 3, per-project since B3). Binds one HTTP +
	// one HTTPS listener per active project's ProjectIP, lazily via
	// /vm/start (RegisterVMHandlers below) — no launchd-inherited
	// sockets and no listeners at daemon startup for a project that
	// isn't running.
	proxy := NewProxyServer(cfg, routes, ca)

	// HTTP API server (Ship 1) with the /routes/* admin endpoints
	// registered on top.
	server := NewServer(cfg.SocketPath(), build)
	RegisterRoutesHandlers(server, routes)

	// VM lifecycle endpoints (Ship 4). Supervisor and tart wrapper are
	// daemon-scoped singletons; the supervisor manages the per-project VM
	// processes and survives across CLI invocations.
	tr := tart.New()
	sup := supervisor.New(cfg.LogDir())
	// Per-project mutex for every state-mutating VM endpoint (start,
	// stop, teardown, reconcile). Serializes concurrent same-project
	// calls inside the daemon instead of relying on the CLI-side flock.
	locks := NewProjectLocks()

	// SNTP responder — one daemon-wide instance, bound eagerly so /vm/start
	// (and discoverSoftnet, below) know the port when they build a softnet
	// endpoint. Under ENFORCED policy, softnet forwards the guest's
	// outbound UDP:123 (timesyncd) to this port; we answer from the host's
	// wall clock. This is what heals guest-clock drift after a Mac sleep —
	// external NTP isn't reachable because our egress firewall doesn't
	// proxy UDP, but the Mac itself is always time-correct. Bound ahead of
	// AdoptIronProxies/discoverSoftnet so its port is ready for the
	// restart-adopt reconcile pass.
	ntp, err := NewNTPServer()
	if err != nil {
		return fmt.Errorf("start ntp responder: %w", err)
	}

	// Adopt iron-proxy processes left running by a prior daemon
	// instance. They survive daemon death by design (setsid on
	// spawn); re-attaching here means /vm/stop and /vm/status
	// behave correctly post-restart instead of orphaning them. It
	// also re-stashes each recovered project's VM IP and rebuilds its
	// direct routes (both otherwise lost on restart), so direct-service
	// DNS keeps working for a VM that's still running.
	// Best-effort — a failure (e.g., `ps` missing) shouldn't
	// block daemon startup.
	if err := AdoptIronProxies(ctx, cfg, sup, tr, routes); err != nil {
		fmt.Fprintf(os.Stderr, "iron-proxy adopt: %v\n", err)
	}

	// Reap softnet processes left behind by a daemon that crashed or was
	// killed mid-project (before /vm/stop's shutdownSoftnet could reach
	// them) — see ReapOrphanSoftnets. Never touches a softnet still
	// serving a live VM: only PPID==1 (parent tart-run already exited)
	// qualifies. Runs in the background; must not block startup.
	ReapOrphanSoftnets(ctx, cfg)

	// Rehydrate softnetState for every project AdoptIronProxies just
	// recovered, and best-effort re-push ENFORCED so the daemon's view
	// and softnet's own policy reconcile after a restart. Must run after
	// AdoptIronProxies — it walks ironProxyState, which the adopt pass
	// above just populated.
	discoverSoftnet(ctx, cfg, ntp.Port())

	// When there are recovered projects to rebind, wait briefly for the
	// helper socket — otherwise skip the gate so first-boot (no projects)
	// doesn't pay the readiness latency. Both daemon and helper get
	// bootstrapped by the same install script, so the daemon can win the
	// race here and see "connection refused" on BindTCP. Timeout is
	// bounded; the retry loop below covers late helpers.
	ids := ironProxyState.keys()
	if len(ids) > 0 {
		waitForHelperReady(ctx, cfg.HelperSocketPath, helperReadinessTimeout)
	}

	// Re-bind this daemon's own per-project HTTP/HTTPS proxy listeners
	// for every project AdoptIronProxies just recovered. Per-project
	// goroutines with a bounded retry — a transient helper hiccup
	// won't strand :80/:443 for the daemon's lifetime.
	for _, id := range ids {
		info, ok := ironProxyState.get(id)
		if !ok || info.ProjectIP == "" {
			continue
		}
		go rebindProjectListeners(ctx, proxy, cfg, id, info.ProjectIP)
	}

	// Denials tracker — per-project counts of iron-proxy allow-list
	// rejects, fed by the supervisor's log tap on iron-proxy stderr.
	// Adopted iron-proxies from a prior daemon instance don't get tapped
	// (we only have their PID, not their output stream), so counts
	// start empty for them until the next SpawnIronProxy respawn.
	denials := NewDenials()

	RegisterVMHandlers(server, cfg, sup, tr, denials, ntp.Port(), locks, proxy)
	RegisterReconcileHandler(server, cfg, locks, &realApplyLiver{tr: tr}, tr, sup)
	RegisterApplyIronProxyHandler(server, cfg, locks, sup, tr, denials)
	RegisterHandshakeHandler(server, cfg, build, sup)
	RegisterStatusAllHandler(server, cfg, sup, tr)

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
		dnsServer := NewDNSServer(cfg, func(project string) (string, bool) {
			info, ok := ironProxyState.get(project)
			if !ok || info.ProjectIP == "" {
				return "", false
			}
			return info.ProjectIP, true
		})
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

	// Reverse proxy readiness (Ship 3). There's no longer a single
	// daemon-wide proxy actor to add to the run group — listeners are
	// per-project and bound lazily by /vm/start (StartProjectListeners)
	// via the helper, not launchd-inherited sockets. The
	// proxy object itself always exists (constructed above), so
	// SetProxyReady lets `devm status`'s /proxy-status probe report
	// "the daemon can serve a proxy" unconditionally instead of
	// TCP-dialing 127.0.0.1:443 (which closes mid-handshake and spams
	// "TLS handshake error … EOF" in the daemon log — the very bug this
	// feedback loop caught).
	server.SetProxyReady(true)

	// Context-cancel actor: when ctx is cancelled (parent signal),
	// the group returns. Also tears down every project's per-project
	// HTTP/HTTPS proxy listeners so a graceful daemon exit doesn't leak
	// bound ports — there's no oklog/run actor for the proxy anymore to
	// do this via its own interrupt func.
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
			proxy.StopAll()
		})
	}

	return g.Run()
}
