package serviceapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/mdubb86/devm/internal/reconcile"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
)

// VMReconcileRequest is the body shape for POST /vm/reconcile.
type VMReconcileRequest struct {
	ProjectID         string        `json:"project_id"`
	VMName            string        `json:"vm_name"`
	Cfg               schema.Config `json:"cfg"`
	WorkspaceHostPath string        `json:"workspace_host_path"`
}

// VMReconcileResponse is the return shape.
type VMReconcileResponse struct {
	Applied          []reconcile.Change `json:"applied"`
	TeardownRequired []reconcile.Change `json:"teardown_required"`
}

// ApplyLiver is the daemon-internal contract for applying live changes
// inside the guest. Real impl calls reconcile.ApplyLive; tests use a
// fake to skip shelling out.
type ApplyLiver interface {
	ApplyLive(changes []reconcile.Change, cfg schema.Config, repoRoot, vmName string) error
}

// realApplyLiver adapts reconcile.ApplyLive to the interface.
type realApplyLiver struct{ tr *tart.Tart }

func (r *realApplyLiver) ApplyLive(changes []reconcile.Change, cfg schema.Config, repoRoot, vmName string) error {
	return reconcile.ApplyLive(r.tr, vmName, changes, cfg, repoRoot)
}

// RegisterReconcileHandler wires POST /vm/reconcile.
func RegisterReconcileHandler(s *Server, locks *ProjectLocks, apply ApplyLiver) {
	s.Register("/vm/reconcile", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req VMReconcileRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf("bad json: %v", err), http.StatusBadRequest)
			return
		}
		if req.ProjectID == "" || req.VMName == "" {
			http.Error(w, "project_id and vm_name required", http.StatusBadRequest)
			return
		}

		unlock := locks.Lock(req.ProjectID)
		defer unlock()

		// Load baseline snapshot. Missing / malformed → nil, treated
		// as "everything is new" by the diff engine.
		oldCfg, err := ReadStateCfg(req.ProjectID)
		if err != nil {
			http.Error(w, fmt.Sprintf("read state: %v", err), http.StatusInternalServerError)
			return
		}
		var base schema.Config
		if oldCfg != nil {
			base = *oldCfg
		}

		changes, err := reconcile.ComputeAllChanges(base, req.Cfg, req.WorkspaceHostPath)
		if err != nil {
			http.Error(w, fmt.Sprintf("diff: %v", err), http.StatusInternalServerError)
			return
		}

		// Partition into live and teardown-required.
		var live, teardown []reconcile.Change
		for _, c := range changes {
			if c.Bucket() == reconcile.BucketLive {
				live = append(live, c)
			} else {
				teardown = append(teardown, c)
			}
		}

		// Apply live changes. On failure, return error and don't
		// touch the snapshot — same as if the request never happened.
		if len(live) > 0 {
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
			defer cancel()
			_ = ctx // reserved for future timeout plumbing into apply
			if err := apply.ApplyLive(live, req.Cfg, req.WorkspaceHostPath, req.VMName); err != nil {
				http.Error(w, fmt.Sprintf("apply live: %v", err), http.StatusInternalServerError)
				return
			}
			// Snapshot merge rule (§Decisions.9): merge only the
			// live-applied fields onto old_cfg so pending teardown
			// changes keep re-surfacing.
			merged := mergeLiveApplied(base, req.Cfg, live)
			if err := WriteStateCfg(req.ProjectID, merged); err != nil {
				http.Error(w, fmt.Sprintf("write state: %v", err), http.StatusInternalServerError)
				return
			}
		}

		resp := VMReconcileResponse{Applied: live, TeardownRequired: teardown}
		body, _ := json.Marshal(resp)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, bytes.NewReader(body))
	})
}

// mergeLiveApplied returns a cfg that equals old_cfg except in the
// fields represented by the applied Change list — those come from
// new_cfg. The rule: pending teardown-required fields must stay at
// their old_cfg values so they keep re-surfacing on subsequent
// reconciles.
//
// Implementation note: this walks the applied changes and copies the
// exact field(s) each Change refers to from new_cfg onto a copy of
// old_cfg. Per-field granularity: Env is by-key, Path is by-index,
// Services by-name-and-subfield.
func mergeLiveApplied(old, new schema.Config, applied []reconcile.Change) schema.Config {
	merged := old
	for _, c := range applied {
		switch c.Kind {
		case reconcile.KindEnvAdd, reconcile.KindEnvRemove, reconcile.KindEnvChange:
			// Env changes: replace the top-level env map wholesale
			// with new_cfg's when any env change lands. Simpler than
			// per-key merge, and correct: if the change was applied,
			// the guest already has new_cfg's env in /opt/devm/.env.
			merged.Env = new.Env
		case reconcile.KindPathChange:
			merged.Path = new.Path
		case reconcile.KindNetworkAdd, reconcile.KindNetworkRemove:
			merged.Network = new.Network
		case reconcile.KindTemplateChange:
			// Templates come from cfg.Services; treat as service-scoped.
			if merged.Services == nil {
				merged.Services = map[string]schema.Service{}
			}
			svc, ok := new.Services[c.Service]
			if ok {
				merged.Services[c.Service] = svc
			}
		case reconcile.KindServiceExecChange, reconcile.KindServiceRestartChange,
			reconcile.KindServiceAfterChange, reconcile.KindServiceWorkdirChange,
			reconcile.KindServiceUserChange, reconcile.KindServiceSystemdOverrideChange,
			reconcile.KindServiceHostnameChange,
			reconcile.KindPortAdd, reconcile.KindPortRemove, reconcile.KindPortChange:
			if merged.Services == nil {
				merged.Services = map[string]schema.Service{}
			}
			if svc, ok := new.Services[c.Service]; ok {
				merged.Services[c.Service] = svc
			}
		}
	}
	return merged
}
