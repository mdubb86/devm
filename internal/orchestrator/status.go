package orchestrator

import (
	"fmt"

	"github.com/mdubb86/devm/internal/sandbox"
	"github.com/mdubb86/devm/internal/schema"
	"gopkg.in/yaml.v3"
)

// RunStatus collects read-only state for `devm status`. Live drift
// detection is RunStatusLive's job (currently stubbed).
func RunStatus(cfg schema.Config, sb *sandbox.Sandbox, repoRoot string) (StatusResult, error) {
	res := StatusResult{Sandbox: sb.Name}
	state := sb.State()
	if state == "" {
		res.State = "absent"
		return res, nil
	}
	res.State = state
	if state != "running" {
		return res, nil
	}

	// Sessions (best-effort).
	if sessions, err := sb.Sessions(); err == nil {
		res.Sessions = sessions
	}

	// Pending changes vs snapshot.
	snapStr, err := ReadSnapshot(sb)
	if err != nil {
		return res, fmt.Errorf("read snapshot: %w", err)
	}
	var snapCfg schema.Config
	if snapStr == "" {
		snapCfg = cfg
	} else {
		if err := yaml.Unmarshal([]byte(snapStr), &snapCfg); err != nil {
			return res, fmt.Errorf("parse snapshot: %w", err)
		}
	}
	statusChanges, err := ComputeAllChanges(snapCfg, cfg, repoRoot)
	if err != nil {
		return res, fmt.Errorf("compute changes: %w", err)
	}
	for _, c := range statusChanges {
		if c.Bucket() == BucketLive {
			res.PendingLive++
		} else {
			res.PendingRecreate++
		}
	}
	return res, nil
}

// RunStatusLive is RunStatus plus a live cross-check of actual sbx
// state against the last-applied snapshot, reporting drift. v1 covers
// PORT drift (the common "someone manually sbx ports --unpublish'd a
// mapping" case). Network/env/mount/image drift are not yet checked —
// they need per-field live queries and kit-var filtering; tracked as
// follow-ups. Port drift alone catches the most frequent real-world
// divergence.
func RunStatusLive(cfg schema.Config, sb *sandbox.Sandbox, repoRoot string) (StatusResult, error) {
	res, err := RunStatus(cfg, sb, repoRoot)
	if err != nil {
		return res, err
	}
	if res.State != "running" {
		return res, nil // nothing live to compare against
	}

	// Baseline = last-applied snapshot (what we believe is deployed).
	snapStr, err := ReadSnapshot(sb)
	if err != nil {
		return res, fmt.Errorf("read snapshot: %w", err)
	}
	var snapCfg schema.Config
	if snapStr == "" {
		snapCfg = cfg
	} else {
		if err := yaml.Unmarshal([]byte(snapStr), &snapCfg); err != nil {
			return res, fmt.Errorf("parse snapshot: %w", err)
		}
	}

	res.Drift = append(res.Drift, portDrift(snapCfg, sb)...)
	return res, nil
}

// portDrift compares the ports the snapshot expects against the ports
// sbx actually reports live. Returns a DriftItem for each mapping that
// is expected-but-missing or live-but-unexpected.
func portDrift(snapCfg schema.Config, sb *sandbox.Sandbox) []DriftItem {
	desired := desiredMappings(snapCfg)
	live, err := currentMappings(sb, sb.Runner)
	if err != nil {
		// Can't query live ports — report as a drift-check failure
		// rather than silently claiming "in sync".
		return []DriftItem{{Kind: "port_check_failed", Detail: err.Error()}}
	}

	liveSet := make(map[int]int) // sandboxPort -> hostPort
	for _, m := range live {
		liveSet[m.SandboxPort] = m.HostPort
	}
	desiredSet := make(map[int]int)
	for _, m := range desired {
		desiredSet[m.SandboxPort] = m.HostPort
	}

	var drift []DriftItem
	for _, m := range desired {
		if _, ok := liveSet[m.SandboxPort]; !ok {
			drift = append(drift, DriftItem{
				Kind:   "port_missing",
				Detail: fmt.Sprintf("expected %d->%d not published live", m.HostPort, m.SandboxPort),
			})
		}
	}
	for _, m := range live {
		if _, ok := desiredSet[m.SandboxPort]; !ok {
			drift = append(drift, DriftItem{
				Kind:   "port_extra",
				Detail: fmt.Sprintf("live %d->%d not in last-applied config", m.HostPort, m.SandboxPort),
			})
		}
	}
	return drift
}
