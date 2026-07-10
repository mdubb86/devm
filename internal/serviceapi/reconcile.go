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
	SandboxState     string             `json:"sandbox_state"` // "running" or "stopped"
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

// TartLister is the subset of *tart.Tart the reconcile handler uses to
// check VM running state before deciding whether to apply live changes.
type TartLister interface {
	List(ctx context.Context) ([]tart.VM, error)
}

// RegisterReconcileHandler wires POST /vm/reconcile.
func RegisterReconcileHandler(s *Server, locks *ProjectLocks, apply ApplyLiver, tr TartLister) {
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

		// Check VM state. If not running, don't apply anything — changes
		// get picked up at next cold-start's provisioner bundle pipe.
		vms, err := tr.List(r.Context())
		if err != nil {
			http.Error(w, fmt.Sprintf("tart list: %v", err), http.StatusInternalServerError)
			return
		}
		running := false
		for _, vm := range vms {
			if vm.Name == req.VMName {
				running = vm.Running
				break
			}
		}

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

		state := "running"
		if !running {
			state = "stopped"
			// Skip apply + snapshot write; return classification only.
			// Changes surface again at cold-start via the provisioner
			// bundle pipe, which will see them via the same diff engine.
			resp := VMReconcileResponse{
				Applied:          nil,
				TeardownRequired: teardown,
				SandboxState:     state,
			}
			body, _ := json.Marshal(resp)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.Copy(w, bytes.NewReader(body))
			return
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

		resp := VMReconcileResponse{Applied: live, TeardownRequired: teardown, SandboxState: state}
		body, _ := json.Marshal(resp)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, bytes.NewReader(body))
	})
}

// mergeLiveApplied returns a cfg that equals old_cfg except in the
// exact fields represented by the applied Change list — those come
// from new_cfg. Pending teardown-required fields on the same service
// (or elsewhere) MUST stay at their old_cfg values so they keep
// re-surfacing on subsequent reconciles.
//
// Granularity: env is by-key (top-level or per-service); path is
// wholesale; network by-list-membership; per-service subfield changes
// touch ONLY that subfield on the service.
func mergeLiveApplied(old, new schema.Config, applied []reconcile.Change) schema.Config {
	merged := old
	// Copy service map before mutating so we don't alias old_cfg's map.
	if len(applied) > 0 && merged.Services != nil {
		copied := make(map[string]schema.Service, len(merged.Services))
		for k, v := range merged.Services {
			copied[k] = v
		}
		merged.Services = copied
	}

	for _, c := range applied {
		switch c.Kind {
		case reconcile.KindEnvAdd, reconcile.KindEnvRemove, reconcile.KindEnvChange:
			if c.Service == "" {
				// Global env change.
				merged.Env = new.Env
			} else {
				// Per-service env change — replace only that service's Env map.
				svc := merged.Services[c.Service]
				if newSvc, ok := new.Services[c.Service]; ok {
					svc.Env = newSvc.Env
				} else {
					// Service was removed in new_cfg; drop it from merged too.
					delete(merged.Services, c.Service)
					continue
				}
				if merged.Services == nil {
					merged.Services = map[string]schema.Service{}
				}
				merged.Services[c.Service] = svc
			}
		case reconcile.KindPathChange:
			merged.Path = new.Path
		case reconcile.KindNetworkAdd, reconcile.KindNetworkRemove:
			merged.Network = new.Network
		case reconcile.KindTemplateChange:
			svc := merged.Services[c.Service]
			if newSvc, ok := new.Services[c.Service]; ok {
				svc.Templates = newSvc.Templates
			} else {
				delete(merged.Services, c.Service)
				continue
			}
			if merged.Services == nil {
				merged.Services = map[string]schema.Service{}
			}
			merged.Services[c.Service] = svc
		case reconcile.KindServiceExecChange:
			svc := merged.Services[c.Service]
			if newSvc, ok := new.Services[c.Service]; ok {
				svc.Exec = newSvc.Exec
				merged.Services[c.Service] = svc
			}
		case reconcile.KindServiceRestartChange:
			svc := merged.Services[c.Service]
			if newSvc, ok := new.Services[c.Service]; ok {
				svc.Restart = newSvc.Restart
				merged.Services[c.Service] = svc
			}
		case reconcile.KindServiceAfterChange:
			svc := merged.Services[c.Service]
			if newSvc, ok := new.Services[c.Service]; ok {
				svc.After = newSvc.After
				merged.Services[c.Service] = svc
			}
		case reconcile.KindServiceWorkdirChange:
			svc := merged.Services[c.Service]
			if newSvc, ok := new.Services[c.Service]; ok {
				svc.WorkDir = newSvc.WorkDir
				merged.Services[c.Service] = svc
			}
		case reconcile.KindServiceUserChange:
			svc := merged.Services[c.Service]
			if newSvc, ok := new.Services[c.Service]; ok {
				svc.User = newSvc.User
				merged.Services[c.Service] = svc
			}
		case reconcile.KindServiceSystemdOverrideChange:
			svc := merged.Services[c.Service]
			if newSvc, ok := new.Services[c.Service]; ok {
				svc.Systemd = newSvc.Systemd
				merged.Services[c.Service] = svc
			}
		case reconcile.KindServiceHostnameChange:
			svc := merged.Services[c.Service]
			if newSvc, ok := new.Services[c.Service]; ok {
				svc.Hostname = newSvc.Hostname
				merged.Services[c.Service] = svc
			}
		case reconcile.KindPortAdd, reconcile.KindPortRemove, reconcile.KindPortChange:
			svc := merged.Services[c.Service]
			if newSvc, ok := new.Services[c.Service]; ok {
				svc.Port = newSvc.Port
				svc.BindIP = newSvc.BindIP
				merged.Services[c.Service] = svc
			}
		}
	}
	return merged
}
