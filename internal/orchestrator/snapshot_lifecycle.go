package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/mdubb86/devm/internal/snapshot"
)

// runLifecycleSnapshots iterates the project's snapshots in declaration
// order and captures every entry whose matching opt-in flag is set:
// OnStop for snapshot.SourceOnStop, OnTeardown for
// snapshot.SourceOnTeardown. Each entry is attempted serially via
// d.ServiceAPIClient.SnapshotCreate.
//
// Failures are collected across all opted-in entries rather than
// short-circuiting the loop — a transient failure of the first entry
// must not mask a second entry that would also have failed. When any
// entry failed, the return error enumerates them one by one. On an
// empty failure list, returns nil.
//
// Callers check d.SkipSnapshot before invoking this; the function
// itself never looks at it.
func runLifecycleSnapshots(ctx context.Context, d StopDeps, projectName string, source snapshot.Source) error {
	var failures []string
	for name, entry := range d.Cfg.Snapshots.Iter() {
		if !lifecycleOptedIn(entry.OnStop, entry.OnTeardown, source) {
			continue
		}
		if _, err := d.ServiceAPIClient.SnapshotCreate(ctx, projectName, name, source); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", name, err))
		}
	}
	if len(failures) == 0 {
		return nil
	}
	return fmt.Errorf("lifecycle snapshots failed: %s", strings.Join(failures, "; "))
}

// lifecycleOptedIn reports whether a snapshot entry is opted in for the
// given source. A nil pointer is "unset" (the schema default) and
// counts as false; only an explicit true opts in.
func lifecycleOptedIn(onStop, onTeardown *bool, source snapshot.Source) bool {
	switch source {
	case snapshot.SourceOnStop:
		return onStop != nil && *onStop
	case snapshot.SourceOnTeardown:
		return onTeardown != nil && *onTeardown
	}
	return false
}
