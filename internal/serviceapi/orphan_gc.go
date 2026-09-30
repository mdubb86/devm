package serviceapi

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/mdubb86/devm/internal/daemonlog"
	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/serviceapi/sshkeys"
)

// GCOrphanedProjects removes on-disk state for projects whose tart VM
// no longer exists. Called once at daemon boot — before
// AdoptIronProxies, the state watchdog warmup, and every other adopt
// loop — because those loops rehydrate the daemon from exactly the
// files this cleans up: KnownProjectNamesForWatchdog() enumerates
// StateDir(cfg) and feeds it straight to the iron-proxy watchdog
// check, which respawns iron-proxy for any project whose observed
// status is ProxyMissing regardless of whether a VM exists for it.
// Left in place, a stale state file (from a project whose VM was
// deleted out from under a still-running daemon — e.g. the e2e
// sweep) causes the daemon to spawn iron-proxy for a ghost project on
// every boot and every watchdog tick after. The e2e sweep then kills
// it, and the watchdog respawns it again within one tick — forever.
//
// The signal is presence-without-a-VM, not VM state: a *stopped* VM
// is still a valid, trackable project. Only a VM tart no longer knows
// about at all is orphaned.
func GCOrphanedProjects(ctx context.Context, cfg identity.Config, tr TartLister) error {
	vms, err := tr.List(ctx)
	if err != nil {
		// Fail-safe: an unknowable VM list must never be treated as an
		// empty one — that would GC every tracked project on a transient
		// `tart list` hiccup. Do nothing and let the caller log it.
		return fmt.Errorf("orphan gc: tart list: %w", err)
	}
	liveVMs := make(map[string]bool, len(vms))
	for _, vm := range vms {
		liveVMs[vm.Name] = true
	}

	baseImage := cfg.BaseImageName()
	for _, projectID := range KnownProjectNamesForWatchdog(cfg) {
		// Defense in depth: base images are never written as project
		// state files (WriteStateSnapshot only ever runs for real
		// projects), so this should never trigger — but a GC routine
		// touching disk gets to be paranoid about what it's about to
		// delete.
		if projectID == baseImage {
			continue
		}
		if liveVMs[projectID] {
			continue
		}
		removeOrphanedProjectArtifacts(cfg, projectID)
		log.Printf("orphan-gc: removed stale project %q (no matching tart VM)", projectID)
	}
	return nil
}

// removeOrphanedProjectArtifacts deletes every on-disk artifact for
// projectID that would otherwise drive a future adopt attempt or
// leave stale files behind: the state snapshot, iron-proxy config +
// policy socket, per-project pending/proposal/approved snapshots, ssh
// project dir, softnet control socket, and every log file the
// supervisor recorded under LogDir for this project (naming pattern
// <projectID>-<role>.log). Each removal is independently best-effort
// (a missing file is not an error) so one absent artifact doesn't
// stop the rest from being cleaned up; failures are logged, never
// swallowed silently.
func removeOrphanedProjectArtifacts(cfg identity.Config, projectID string) {
	removeIfExists(projectID, filepath.Join(StateDir(cfg), projectID+".json"))

	if path, err := IronProxyConfigPath(cfg, projectID); err != nil {
		daemonlog.Errorf("orphan-gc: resolve iron-proxy config path for %q: %v", projectID, err)
	} else {
		removeIfExists(projectID, path)
	}

	if path, err := IronPolicySocketPath(cfg, projectID); err != nil {
		daemonlog.Errorf("orphan-gc: resolve iron-proxy policy socket path for %q: %v", projectID, err)
	} else {
		removeIfExists(projectID, path)
	}

	projectDir := filepath.Join(cfg.RuntimeDir(), projectID)
	removeIfExists(projectID, filepath.Join(projectDir, "pending-passthrough.json"))
	removeIfExists(projectID, filepath.Join(projectDir, "last-proposal.json"))
	removeIfExists(projectID, filepath.Join(projectDir, "approved-snapshot"))

	removeIfExists(projectID, sshkeys.ProjectDir(cfg, projectID))

	// Softnet control socket: a per-project UDS the daemon uses to push
	// expose-map / test-hosts / policy changes to the softnet process.
	// The file lives under softnetSockDir() with a name derived from
	// hash(cfg.RuntimeDir()+projectID) — SoftnetControlSock recomputes
	// it deterministically.
	removeIfExists(projectID, SoftnetControlSock(cfg, projectID))

	// Log files the supervisor recorded for this project. Path pattern
	// is <LogDir>/<projectID>-<role>.log (supervisor.go). Glob catches
	// every role (iron-proxy, softnet, mutagen, etc.) without this GC
	// having to enumerate them.
	if matches, err := filepath.Glob(filepath.Join(cfg.LogDir(), projectID+"-*.log")); err != nil {
		daemonlog.Errorf("orphan-gc: glob log files for %q: %v", projectID, err)
	} else {
		for _, path := range matches {
			removeIfExists(projectID, path)
		}
	}
}

// removeIfExists removes path (file or directory tree), logging any
// error RemoveAll returns. RemoveAll already treats a missing path as
// success — this wrapper's only job is the log line, carrying projectID
// for context.
func removeIfExists(projectID, path string) {
	if err := os.RemoveAll(path); err != nil {
		daemonlog.Errorf("orphan-gc: remove %s (project %q): %v", path, projectID, err)
	}
}
