package orchestrator

import (
	"fmt"

	"github.com/mtwaage/devm/internal/sandbox"
	"github.com/mtwaage/devm/internal/schema"
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
	for _, c := range ComputeAllChanges(snapCfg, cfg) {
		if c.Bucket() == BucketLive {
			res.PendingLive++
		} else {
			res.PendingRecreate++
		}
	}
	return res, nil
}

// RunStatusLive is RunStatus + live drift queries. v1 stub: returns
// plain status with empty Drift. The live cross-checks (sbx ports
// --json, sbx policy ls, etc.) land in a follow-up task — the surface
// is here so the CLI flag wires through without breakage.
func RunStatusLive(cfg schema.Config, sb *sandbox.Sandbox, repoRoot string) (StatusResult, error) {
	return RunStatus(cfg, sb, repoRoot)
}
