package watchdog

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/serviceapi"
)

// fakeGroundTruth is a shared test fake all check_*_test.go files
// use. Only the methods a given test needs get set; the rest panic
// so a test accidentally invoking an unset dependency fails loudly.
type fakeGroundTruth struct {
	mu sync.Mutex

	IronProxyHealthFn func(ctx context.Context, projectID string) serviceapi.ProxyHealth
	RespawnIronFn     func(ctx context.Context, projectID string) error

	MutagenPIDFn     func(dataDir string) (int, error)
	RespawnMutagenFn func(ctx context.Context) error
	DataDir          string
	Projects         []string

	TartListFn func(ctx context.Context) ([]tart.VM, error)

	ApproveHashFn  func(macCwd string) (string, string, error)
	ReadSnapshotFn func(projectID string) (string, string, *time.Time, bool, error)

	PopSummaryFn func(projectID string) serviceapi.PopSessionSummary
}

func (f *fakeGroundTruth) IronProxyHealth(ctx context.Context, projectID string) serviceapi.ProxyHealth {
	if f.IronProxyHealthFn == nil {
		panic("fake: IronProxyHealthFn not set")
	}
	return f.IronProxyHealthFn(ctx, projectID)
}

func (f *fakeGroundTruth) RespawnIronProxy(ctx context.Context, projectID string) error {
	if f.RespawnIronFn == nil {
		return errors.New("fake: RespawnIronFn not set")
	}
	return f.RespawnIronFn(ctx, projectID)
}

func (f *fakeGroundTruth) MutagenLockPID(dataDir string) (int, error) {
	if f.MutagenPIDFn == nil {
		panic("fake: MutagenPIDFn not set")
	}
	return f.MutagenPIDFn(dataDir)
}

func (f *fakeGroundTruth) RespawnMutagenDaemon(ctx context.Context) error {
	if f.RespawnMutagenFn == nil {
		return errors.New("fake: RespawnMutagenFn not set")
	}
	return f.RespawnMutagenFn(ctx)
}

func (f *fakeGroundTruth) MutagenDataDir() string { return f.DataDir }

func (f *fakeGroundTruth) KnownProjectNames() []string { return f.Projects }

func (f *fakeGroundTruth) TartList(ctx context.Context) ([]tart.VM, error) {
	if f.TartListFn == nil {
		panic("fake: TartListFn not set")
	}
	return f.TartListFn(ctx)
}

func (f *fakeGroundTruth) ApproveHash(macCwd string) (string, string, error) {
	if f.ApproveHashFn == nil {
		panic("fake: ApproveHashFn not set")
	}
	return f.ApproveHashFn(macCwd)
}

func (f *fakeGroundTruth) ReadApprovedSnapshot(projectID string) (string, string, *time.Time, bool, error) {
	if f.ReadSnapshotFn == nil {
		panic("fake: ReadSnapshotFn not set")
	}
	return f.ReadSnapshotFn(projectID)
}

func (f *fakeGroundTruth) PopSessionSummaryForProject(projectID string) serviceapi.PopSessionSummary {
	if f.PopSummaryFn == nil {
		panic("fake: PopSummaryFn not set")
	}
	return f.PopSummaryFn(projectID)
}

var _ GroundTruth = (*fakeGroundTruth)(nil)
