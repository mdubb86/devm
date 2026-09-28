package serviceapi

import (
	"bytes"
	"context"
	"fmt"

	"github.com/mdubb86/devm/internal/devmbundle"
	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/sandbox/tart"
)

// RefreshSummary describes the outcome of a bundle refresh. Returned
// from RefreshGuestBundle so callers (the HTTP handler, the daemon-
// start catchup, the gdevm CLI via HTTP response) can present a
// consistent summary.
type RefreshSummary struct {
	ProjectID      string `json:"project_id"`
	OldFingerprint string `json:"old_fingerprint"`
	NewFingerprint string `json:"new_fingerprint"`
	GdevmBytes     int    `json:"gdevm_bytes"`
}

// RefreshGuestBundle rebuilds the provisioning bundle for projectID
// from the last-applied cfg (StateSnapshot.Cfg — approve-gate safe;
// see the spec's approve-gate section) and pipes it into the
// running VM via the same tart-exec-stdin path apply_live already
// uses for reconcile-driven bundle rebuilds. Writes back an updated
// StateSnapshot with the new BundleFingerprint on success.
//
// Errors from the tart exec surface directly and leave the snapshot
// untouched — a next reconcile / gdevm upgrade will retry.
//
// Not approve-gated: the caller is asking to refresh what the daemon
// would ship anyway. The gate lives on the config that gets rendered
// into the bundle; RefreshGuestBundle uses the already-approved cfg
// stored in the snapshot, so nothing un-reviewed can reach the guest.
func RefreshGuestBundle(cfg identity.Config, cache *StateCache, tr *tart.Tart, projectID string) (RefreshSummary, error) {
	snap, err := ReadStateSnapshot(cfg, projectID)
	if err != nil {
		return RefreshSummary{}, fmt.Errorf("refresh-bundle: read state: %w", err)
	}
	if snap == nil {
		return RefreshSummary{}, fmt.Errorf("refresh-bundle: no state snapshot for %q — VM must be started first via `devm start`", projectID)
	}

	// Resolve daemon runtime dir + per-project material the bundle
	// installer needs. Everything downstream that reads on-disk CA /
	// SSH files uses cfg.RuntimeDir() as the root.
	daemonRuntimeDir, err := EnsureRuntimeDir(cfg)
	if err != nil {
		return RefreshSummary{}, fmt.Errorf("refresh-bundle: runtime dir: %w", err)
	}
	// The refresh does not re-derive per-project SSH material or
	// re-generate the CA — the running guest already trusts them
	// and they haven't changed. Pass nil; devmbundle.Build's writers
	// skip empty entries.
	in, err := devmbundle.BuildInputFor(snap.Cfg, "", daemonRuntimeDir, nil, nil, nil, nil)
	if err != nil {
		return RefreshSummary{}, fmt.Errorf("refresh-bundle: build input: %w", err)
	}

	tarBytes, err := devmbundle.Build(in)
	if err != nil {
		return RefreshSummary{}, fmt.Errorf("refresh-bundle: build bundle: %w", err)
	}

	r := tr.ExecStdin(context.Background(), projectID,
		bytes.NewReader(tarBytes),
		[]string{"bash", "-e", "-o", "pipefail", "-c", devmbundle.GuestInstallScript},
	)
	if r.ExitCode != 0 {
		return RefreshSummary{}, fmt.Errorf("refresh-bundle: pipe bundle: exit %d (stderr: %s)", r.ExitCode, r.Stderr)
	}

	// Success — update the snapshot fingerprint.
	oldFP := snap.BundleFingerprint
	newFP := CurrentBundleFingerprint(cache)
	snap.BundleFingerprint = newFP
	if err := WriteStateSnapshot(cfg, projectID, *snap); err != nil {
		return RefreshSummary{}, fmt.Errorf("refresh-bundle: write state: %w", err)
	}

	return RefreshSummary{
		ProjectID:      projectID,
		OldFingerprint: oldFP,
		NewFingerprint: newFP,
		GdevmBytes:     len(in.Gdevm),
	}, nil
}
