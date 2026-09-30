package serviceapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/supervisor"
)

// GroundTruth is the test-injection seam for watchdog checks. Each
// check reads only the methods it needs, so a fake in a test can
// provide the minimal set. Populated in full by the real impl below.
type GroundTruth interface {
	IronProxyHealth(ctx context.Context, projectID string) ProxyHealth
	RespawnIronProxy(ctx context.Context, projectID string) error

	MutagenLockPID(dataDir string) (int, error)
	RespawnMutagenDaemon(ctx context.Context) error
	MutagenDataDir() string
	KnownProjectNames() []string

	TartList(ctx context.Context) ([]tart.VM, error)

	ApproveHash(projectID, macCwd string) (currentDevmSHA, currentMeSHA string, err error)
	ReadApprovedSnapshot(projectID string) (devmSHA, meSHA string, since *time.Time, hasSnap bool, err error)

	ProxyListenerHealth(ctx context.Context, projectID string) bool
	RespawnProxyListeners(ctx context.Context, projectID string) error
}

// RealGroundTruth is the production implementation. Each method is
// filled in by its owning check's task; unimplemented methods panic
// so a wiring mistake surfaces immediately in dev. Locks must be set
// to the daemon's shared *ProjectLocks (runner.go) — RespawnIronProxy
// takes the per-project reconcile lock so a watchdog respawn can't
// race a concurrent /vm/start or /vm/reconcile.
type RealGroundTruth struct {
	Cfg   identity.Config
	Tart  *tart.Tart
	Sup   *supervisor.Supervisor
	Proxy *ProxyServer
	Locks *ProjectLocks
}

func (g *RealGroundTruth) IronProxyHealth(ctx context.Context, projectID string) ProxyHealth {
	return ComputeProxyHealth(g.Cfg, g.Sup, g.Proxy, projectID)
}

func (g *RealGroundTruth) RespawnIronProxy(ctx context.Context, projectID string) error {
	return RespawnIronProxyForWatchdog(ctx, g.Cfg, g.Sup, g.Proxy, projectID, g.Locks)
}

func (g *RealGroundTruth) MutagenLockPID(dataDir string) (int, error) {
	return MutagenLockPIDForWatchdog(dataDir)
}

func (g *RealGroundTruth) RespawnMutagenDaemon(ctx context.Context) error {
	return RespawnMutagenForWatchdog(ctx, g.Cfg, g.Sup)
}

func (g *RealGroundTruth) MutagenDataDir() string {
	return MutagenDataDirForWatchdog(g.Cfg)
}

func (g *RealGroundTruth) KnownProjectNames() []string {
	return KnownProjectNamesForWatchdog(g.Cfg)
}

func (g *RealGroundTruth) TartList(ctx context.Context) ([]tart.VM, error) {
	return g.Tart.List(ctx)
}

func (g *RealGroundTruth) ApproveHash(projectID, macCwd string) (string, string, error) {
	return HashCurrentFilesForWatchdog(macCwd)
}

func (g *RealGroundTruth) ReadApprovedSnapshot(projectID string) (string, string, *time.Time, bool, error) {
	return ReadApprovedSnapshotForWatchdog(g.Cfg, projectID)
}

// healthProbeClient is the watchdog's HTTP client for probing a
// project's reverse-proxy listener pair via its reserved
// _devm.<project>.<tld> route (see reservedHealthRoute in routes.go).
// Short timeout and no redirect following — nothing behind /v1/health
// legitimately redirects, so a probe that can't complete in 2s or
// that redirects is unhealthy.
//
// Its own Transport (not http.DefaultTransport) with a per-host idle
// pool of 1 and a 30s idle timeout keeps a growing project count from
// piling up long-lived idle connections in the shared default pool.
// ProxyListenerHealth drains each response body before Close so the
// one pooled connection is eligible for reuse.
var healthProbeClient = &http.Client{
	Timeout: 2 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
	Transport: &http.Transport{
		MaxIdleConns:        16,
		MaxIdleConnsPerHost: 1,
		IdleConnTimeout:     30 * time.Second,
	},
}

// proxyHealthProbeResponse mirrors the field this probe needs from
// cmd/gdevm's healthResponse (package main there, unimportable here).
type proxyHealthProbeResponse struct {
	OK bool `json:"ok"`
}

// ProxyListenerHealth probes projectID's reverse-proxy listener pair
// by dialing its allocated project IP directly (not through DNS) and
// setting the Host header to its reserved _devm.<project>.<tld>
// hostname, so the request exercises the same routing path a real
// browser hit would. Any transport error, non-200 status, or a body
// that doesn't decode to {"ok":true} counts as unhealthy.
func (g *RealGroundTruth) ProxyListenerHealth(ctx context.Context, projectID string) bool {
	info, ok := ironProxyState.get(projectID)
	if !ok || info.ProjectIP == "" {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+info.ProjectIP+"/v1/health", nil)
	if err != nil {
		return false
	}
	req.Host = "_devm." + projectID + "." + g.Cfg.TLD
	resp, err := healthProbeClient.Do(req)
	if err != nil {
		return false
	}
	defer func() {
		// Drain to EOF before Close so the transport can reuse the
		// underlying connection instead of opening a fresh one per probe.
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var body proxyHealthProbeResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return false
	}
	return body.OK
}

// RespawnProxyListeners tears down and rebinds projectID's :80/:443
// listener pair. Used by ProxyListenerCheck when a health probe fails
// twice in a row (see watchdog_check_proxy_listener.go).
//
// Takes the per-project reconcile lock so a watchdog respawn can't
// race a concurrent /vm/stop: /vm/stop holds the same lock across its
// whole teardown sequence (StopProjectListeners, IP release, and
// ironProxyState.del — vm.go), so re-checking ironProxyState under
// this lock is guaranteed to observe either the pre-stop state or the
// fully-torn-down state, never a state in between.
func (g *RealGroundTruth) RespawnProxyListeners(ctx context.Context, projectID string) error {
	unlock := g.Locks.Lock(projectID)
	defer unlock()

	info, ok := ironProxyState.get(projectID)
	if !ok || info.ProjectIP == "" {
		// The project was torn down (or never had a claimed IP) while
		// we waited for the lock — nothing to respawn.
		return nil
	}

	g.Proxy.StopProjectListeners(projectID)
	return g.Proxy.StartProjectListeners(ctx, projectID, info.ProjectIP)
}

var _ GroundTruth = (*RealGroundTruth)(nil)
