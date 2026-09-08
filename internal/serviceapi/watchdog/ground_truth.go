package watchdog

import (
	"context"
	"time"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/mutagen"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/mdubb86/devm/internal/supervisor"
)

// GroundTruth is the test-injection seam for watchdog checks. Each
// check reads only the methods it needs, so a fake in a test can
// provide the minimal set. Populated in full by the real impl below.
type GroundTruth interface {
	IronProxyHealth(ctx context.Context, projectID string) serviceapi.ProxyHealth
	RespawnIronProxy(ctx context.Context, projectID string) error

	MutagenLockPID(dataDir string) (int, error)
	RespawnMutagenDaemon(ctx context.Context) error
	MutagenDataDir() string
	KnownProjectNames() []string

	TartList(ctx context.Context) ([]tart.VM, error)

	ApproveHash(macCwd string) (currentDevmSHA, currentMeSHA string, err error)
	ReadApprovedSnapshot(projectID string) (devmSHA, meSHA string, since *time.Time, hasSnap bool, err error)

	PopSessionSummaryForProject(projectID string) serviceapi.PopSessionSummary
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
	Proxy      *serviceapi.ProxyServer
	MutagenCLI *mutagen.CLI
	PopStore   *serviceapi.PopSessionStore
	Locks      *serviceapi.ProjectLocks
}

func (g *RealGroundTruth) IronProxyHealth(ctx context.Context, projectID string) serviceapi.ProxyHealth {
	return serviceapi.ComputeProxyHealth(g.Cfg, g.Sup, g.Proxy, projectID)
}

func (g *RealGroundTruth) RespawnIronProxy(ctx context.Context, projectID string) error {
	return serviceapi.RespawnIronProxyForWatchdog(ctx, g.Cfg, g.Sup, g.Proxy, projectID, g.Locks)
}

func (g *RealGroundTruth) MutagenLockPID(dataDir string) (int, error) {
	return serviceapi.MutagenLockPIDForWatchdog(dataDir)
}

func (g *RealGroundTruth) RespawnMutagenDaemon(ctx context.Context) error {
	return serviceapi.RespawnMutagenForWatchdog(ctx, g.Cfg, g.Sup)
}

func (g *RealGroundTruth) MutagenDataDir() string {
	return serviceapi.MutagenDataDirForWatchdog(g.Cfg)
}

func (g *RealGroundTruth) KnownProjectNames() []string {
	return serviceapi.KnownProjectNamesForWatchdog(g.Cfg)
}

func (g *RealGroundTruth) TartList(ctx context.Context) ([]tart.VM, error) {
	return g.Tart.List(ctx)
}

func (g *RealGroundTruth) ApproveHash(macCwd string) (string, string, error) {
	return serviceapi.HashCurrentFilesForWatchdog(macCwd)
}

func (g *RealGroundTruth) ReadApprovedSnapshot(projectID string) (string, string, *time.Time, bool, error) {
	return serviceapi.ReadApprovedSnapshotForWatchdog(g.Cfg, projectID)
}

func (g *RealGroundTruth) PopSessionSummaryForProject(projectID string) serviceapi.PopSessionSummary {
	return serviceapi.PopSessionSummaryForProjectForWatchdog(g.PopStore, projectID)
}

var _ GroundTruth = (*RealGroundTruth)(nil)
