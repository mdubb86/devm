package serviceapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/mdubb86/devm/internal/daemonlog"
	"github.com/mdubb86/devm/internal/devmbundle"
	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/sandbox/tart"
)

// ErrNoStateSnapshot is returned by RefreshGuestBundle when the project
// has no stored StateSnapshot — the VM has never been started, so there
// is no baseline cfg to rebuild the bundle from. The HTTP handler
// dispatches this precondition to 412 Precondition Failed via errors.Is
// rather than string-matching the error message.
var ErrNoStateSnapshot = errors.New("no state snapshot")

// bundleCatchupPerProjectTimeout caps how long the startup drift sweep
// waits on any one project's guest-agent pipe. A wedged guest agent
// past this deadline is skipped so the sweep can move on and the
// daemon can announce itself ready.
//
// var, not const, so the timeout-path test can shorten it — the
// production default is the only value ever set at runtime.
var bundleCatchupPerProjectTimeout = 60 * time.Second

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
//
// ctx bounds the tart-exec pipe into the guest. Callers pass an HTTP
// request context (client-disconnect cancels the pipe) or a bounded
// startup-sweep context (a hung guest agent is skipped after the
// deadline, not blocking the whole daemon).
func RefreshGuestBundle(ctx context.Context, cfg identity.Config, cache *StateCache, tr *tart.Tart, projectID string) (RefreshSummary, error) {
	snap, err := ReadStateSnapshot(cfg, projectID)
	if err != nil {
		return RefreshSummary{}, fmt.Errorf("refresh-bundle: read state: %w", err)
	}
	if snap == nil {
		return RefreshSummary{}, fmt.Errorf("refresh-bundle: %w: for %q — VM must be started first via `devm start`", ErrNoStateSnapshot, projectID)
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

	r := tr.ExecStdin(ctx, projectID,
		bytes.NewReader(tarBytes),
		[]string{"bash", "-e", "-o", "pipefail", "-c", devmbundle.GuestInstallScript},
	)
	if r.ExitCode != 0 {
		// tart.ExecStdin swallows exec.CommandContext's kill reason,
		// so a caller-side deadline surfaces here as exit code -1
		// with no distinguishing marker. Consult ctx.Err() so the
		// caller can tell a hung guest agent apart from a real
		// non-zero exit.
		if err := ctx.Err(); err != nil {
			return RefreshSummary{}, fmt.Errorf("refresh-bundle: pipe bundle: %w", err)
		}
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

// handleRefreshBundleForProject returns the per-project
// POST /refresh-bundle handler serving the guest side (softnet:82,
// shared listener with /propose and /passthrough — see
// serveGuestAPIListener). Body is ignored (no metadata needed —
// the refresh is not a request, it's a maintenance ping).
//
// Acquires locks.Lock(projectName) before invoking RefreshGuestBundle
// so a concurrent /vm/reconcile can't race the StateSnapshot
// read-modify-write on the same project.
//
// Response is a human-readable summary of the refresh, echoed to
// gdevm upgrade's stdout.
func handleRefreshBundleForProject(cfg identity.Config, cache *StateCache, tr *tart.Tart, locks *ProjectLocks, projectName string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "refresh-bundle: POST only", http.StatusMethodNotAllowed)
			return
		}
		unlock := locks.Lock(projectName)
		defer unlock()
		summary, err := RefreshGuestBundle(r.Context(), cfg, cache, tr, projectName)
		if err != nil {
			// Distinguish the missing-snapshot precondition from
			// operational errors so callers get an actionable
			// status code.
			if errors.Is(err, ErrNoStateSnapshot) {
				http.Error(w, err.Error(), http.StatusPreconditionFailed)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "bundle refreshed for %s\n  old fingerprint: %s\n  new fingerprint: %s\n  gdevm bytes:     %d\n",
			summary.ProjectID, summary.OldFingerprint, summary.NewFingerprint, summary.GdevmBytes)
	})
}

// BundleDriftCatchup iterates the cache's known projects and refreshes
// the bundle for every running project whose stored
// StateSnapshot.BundleFingerprint differs from the daemon's current
// Fingerprint. Best-effort: an error against one project logs and the
// sweep continues to the next, so one broken project can't stall the
// startup path.
//
// Takes locks so each per-project refresh queues against a concurrent
// /vm/reconcile or /refresh-bundle on the same project — same
// mutual-exclusion contract those handlers already honor when they
// call RefreshGuestBundle. Startup ordering keeps this sweep serial
// with the HTTP accept loop today, but a future refactor that binds
// the listener earlier would race the same StateSnapshot read-modify-
// write without this lock.
//
// Called synchronously from RunService's startup path after
// AdoptIronProxies, so `devm status` reflects the settled post-refresh
// state on the first query after the daemon becomes healthy.
func BundleDriftCatchup(cfg identity.Config, cache *StateCache, tr *tart.Tart, locks *ProjectLocks) {
	current := CurrentBundleFingerprint(cache)
	if current == "" {
		return
	}
	for projectID, row := range cache.AllProjectRows() {
		if row.VMState != VMRunning {
			continue
		}
		snap, err := ReadStateSnapshot(cfg, projectID)
		if err != nil {
			daemonlog.Errorf("refresh-bundle: startup catchup: read snapshot for %s: %v", projectID, err)
			continue
		}
		if snap == nil || snap.BundleFingerprint == current {
			continue
		}
		oldFP := snap.BundleFingerprint
		unlock := locks.Lock(projectID)
		err = refreshOneForCatchup(cfg, cache, tr, projectID)
		unlock()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				daemonlog.Errorf("refresh-bundle: startup catchup: sweep-timeout %s after %s: %v", projectID, bundleCatchupPerProjectTimeout, err)
			} else {
				daemonlog.Errorf("refresh-bundle: startup catchup: refresh %s: %v", projectID, err)
			}
			continue
		}
		log.Printf("refresh-bundle: startup catchup: refreshed %s (%s -> %s)",
			projectID, oldFP, current)
	}
}

// refreshOneForCatchup wraps a single project's RefreshGuestBundle in
// a bounded context so a wedged guest agent gets skipped after
// bundleCatchupPerProjectTimeout instead of stalling the startup
// sweep. Extracted so the deferred cancel scopes to one iteration.
func refreshOneForCatchup(cfg identity.Config, cache *StateCache, tr *tart.Tart, projectID string) error {
	pctx, cancel := context.WithTimeout(context.Background(), bundleCatchupPerProjectTimeout)
	defer cancel()
	_, err := RefreshGuestBundle(pctx, cfg, cache, tr, projectID)
	return err
}
