package serviceapi

import (
	"context"
	"log"
	"time"

	"github.com/mdubb86/devm/internal/daemonlog"
	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/supervisor"
)

// mutagenWatchdogInterval is how often the watchdog checks the mutagen
// daemon's liveness. Same cadence as the iron-proxy watchdog: short
// enough that a silently-dead sync daemon (SIGKILL, hard crash) is
// noticed quickly; long enough to keep the per-tick status read off
// the CPU profile.
const mutagenWatchdogInterval = 30 * time.Second

// spawnMutagenFn is the test-injection seam for SpawnMutagen.
var spawnMutagenFn = SpawnMutagen

// StartMutagenWatchdog launches a background goroutine that polls the
// mutagen daemon's supervised state every 30s and respawns it if it has
// silently died. Returns immediately; the goroutine exits once ctx is
// done.
func StartMutagenWatchdog(ctx context.Context, cfg identity.Config, sup *supervisor.Supervisor) {
	go func() {
		_ = runMutagenWatchdog(ctx, cfg, sup)
	}()
}

// runMutagenWatchdog is the blocking poll loop, extracted from
// StartMutagenWatchdog so tests can drive ctx cancellation directly
// instead of racing a background goroutine.
func runMutagenWatchdog(ctx context.Context, cfg identity.Config, sup *supervisor.Supervisor) error {
	tick := time.NewTicker(mutagenWatchdogInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			if err := healMutagen(ctx, cfg, sup); err != nil {
				daemonlog.Errorf("serviceapi: mutagen watchdog: %v", err)
			}
		}
	}
}

// healMutagen is one watchdog pass: if the mutagen daemon is missing
// from the supervisor, respawn it. Extracted from runMutagenWatchdog so
// tests can drive one tick without a goroutine + ticker.
func healMutagen(ctx context.Context, cfg identity.Config, sup *supervisor.Supervisor) error {
	key := supervisor.Key{Role: supervisor.RoleMutagen}
	if sup.Status(key).Present {
		return nil
	}
	if err := spawnMutagenFn(ctx, cfg, sup); err != nil {
		return err
	}
	log.Printf("serviceapi: mutagen watchdog: respawned mutagen daemon (was missing)")
	return nil
}
