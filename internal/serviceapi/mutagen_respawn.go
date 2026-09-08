package serviceapi

import (
	"context"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/supervisor"
)

// spawnMutagenFn is the test-injection seam for SpawnMutagen.
var spawnMutagenFn = SpawnMutagen

// RespawnMutagenForWatchdog respawns the mutagen daemon if it is not
// already present under supervision. Used by the state-cache
// watchdog's mutagen check.
func RespawnMutagenForWatchdog(ctx context.Context, cfg identity.Config, sup *supervisor.Supervisor) error {
	key := supervisor.Key{Role: supervisor.RoleMutagen}
	if sup.Status(key).Present {
		return nil
	}
	return spawnMutagenFn(ctx, cfg, sup)
}
