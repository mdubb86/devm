package serviceapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/mutagen"
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

	PopSessionSummaryForProject(projectID string) PopSessionSummary

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
	Cfg        identity.Config
	Tart       *tart.Tart
	Sup        *supervisor.Supervisor
	Proxy      *ProxyServer
	MutagenCLI *mutagen.CLI
	PopStore   *PopSessionStore
	Locks      *ProjectLocks
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

func (g *RealGroundTruth) PopSessionSummaryForProject(projectID string) PopSessionSummary {
	return PopSessionSummaryForProjectForWatchdog(g.PopStore, projectID)
}

// healthProbeClient is the watchdog's HTTP client for probing a
// project's reverse-proxy listener pair via its reserved
// _devm.<project>.test route (see reservedHealthRoute in routes.go).
// Short timeout and no redirect following — nothing behind /v1/health
// legitimately redirects, so a probe that can't complete in 2s or
// that redirects is unhealthy. Shared across probes: Go's default
// transport pools connections per host, so reuse is fine here.
var healthProbeClient = &http.Client{
	Timeout: 2 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// proxyHealthProbeResponse mirrors the field this probe needs from
// cmd/gdevm's healthResponse (package main there, unimportable here).
type proxyHealthProbeResponse struct {
	OK bool `json:"ok"`
}

// ProxyListenerHealth probes projectID's reverse-proxy listener pair
// by dialing its allocated project IP directly (not through DNS) and
// setting the Host header to its reserved _devm.<project>.test
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
	req.Host = "_devm." + projectID + ".test"
	resp, err := healthProbeClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
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
func (g *RealGroundTruth) RespawnProxyListeners(ctx context.Context, projectID string) error {
	g.Proxy.StopProjectListeners(projectID)
	info, ok := ironProxyState.get(projectID)
	if !ok || info.ProjectIP == "" {
		return fmt.Errorf("respawn proxy listeners for %s: no project IP recorded", projectID)
	}
	return g.Proxy.StartProjectListeners(ctx, projectID, info.ProjectIP)
}

var _ GroundTruth = (*RealGroundTruth)(nil)
