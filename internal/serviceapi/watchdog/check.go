package watchdog

import (
	"context"

	"github.com/mdubb86/devm/internal/serviceapi"
)

// Check is one structured check the state watchdog performs on each
// pass. Read expected state from cache, observe ground truth,
// compare, on drift apply the check's repair policy (which may be
// no-op), then update the cache with the final observed state.
type Check interface {
	Name() string
	Run(ctx context.Context, cache *serviceapi.StateCache, gt GroundTruth) (drifted bool, err error)
}
