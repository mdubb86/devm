package serviceapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/mdubb86/devm/internal/debuglog"
	"github.com/mdubb86/devm/internal/reconcile"
	"github.com/mdubb86/devm/internal/render"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/supervisor"
)

// VMReconcileRequest is the body shape for POST /vm/reconcile.
type VMReconcileRequest struct {
	Name              string        `json:"name"`
	Cfg               schema.Config `json:"cfg"`
	WorkspaceHostPath string        `json:"workspace_host_path"`
	// SecretHashes is {name: hex sha256(resolved value)} for every
	// !secret ref in cfg. The CLI resolves + hashes secrets (login-
	// keychain access happens in the user context) and sends the map
	// here. Empty or nil means "no secrets to consider" — safe for old
	// clients.
	SecretHashes        map[string]string `json:"secret_hashes,omitempty"`
	SSHAuthorizedPubkey []byte            `json:"ssh_authorized_pubkey,omitempty"`
	SSHHostPriv         []byte            `json:"ssh_host_priv,omitempty"`
	SSHHostPub          []byte            `json:"ssh_host_pub,omitempty"`
}

// VMReconcileResponse is the return shape.
type VMReconcileResponse struct {
	Applied []reconcile.Change `json:"applied"`
	// AppliedIronProxy carries changes in BucketEgressRestart that the
	// daemon has NOT applied — the CLI dispatches /vm/apply-iron-proxy
	// after resolving the current allowlist + secrets in the user
	// context. The daemon never writes SecretHashes for these changes;
	// only a successful /vm/apply-iron-proxy call does that.
	AppliedIronProxy []reconcile.Change `json:"applied_iron_proxy,omitempty"`
	TeardownRequired []reconcile.Change `json:"teardown_required"`
	SandboxState     string             `json:"sandbox_state"` // "running" or "stopped"
}

// ApplyLiver is the daemon-internal contract for applying live changes
// inside the guest. Real impl calls reconcile.ApplyLive; tests use a
// fake to skip shelling out.
type ApplyLiver interface {
	ApplyLive(changes []reconcile.Change, cfg schema.Config, repoRoot, vmName string, caPEM, sshAuthPub, sshHostPriv, sshHostPub []byte) error
}

// realApplyLiver adapts reconcile.ApplyLive to the interface.
type realApplyLiver struct{ tr *tart.Tart }

func (r *realApplyLiver) ApplyLive(changes []reconcile.Change, cfg schema.Config, repoRoot, vmName string, caPEM, sshAuthPub, sshHostPriv, sshHostPub []byte) error {
	return reconcile.ApplyLive(r.tr, vmName, changes, cfg, repoRoot, caPEM, sshAuthPub, sshHostPriv, sshHostPub)
}

// TartLister is the subset of *tart.Tart the reconcile handler uses to
// check VM running state before deciding whether to apply live changes.
type TartLister interface {
	List(ctx context.Context) ([]tart.VM, error)
}

// RegisterReconcileHandler wires POST /vm/reconcile. sup is consulted
// (only when the VM is running) to self-heal a missing/stale
// iron-proxy: see the KindIronProxyDown emit below.
func RegisterReconcileHandler(s *Server, locks *ProjectLocks, apply ApplyLiver, tr TartLister, sup *supervisor.Supervisor) {
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
		if req.Name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}

		unlock := locks.Lock(req.Name)
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
			if vm.Name == req.Name {
				running = vm.Running
				break
			}
		}

		// Load baseline snapshot. Missing / malformed → nil, treated
		// as "everything is new" by the diff engine.
		oldSnap, err := ReadStateSnapshot(req.Name)
		if err != nil {
			http.Error(w, fmt.Sprintf("read state: %v", err), http.StatusInternalServerError)
			return
		}
		var base schema.Config
		var lastAppliedTemplates map[string]string
		var oldSecretHashes map[string]string
		if oldSnap != nil {
			base = oldSnap.Cfg
			lastAppliedTemplates = oldSnap.TemplateContents
			oldSecretHashes = oldSnap.SecretHashes
		}

		changes, err := reconcile.ComputeAllChanges(
			base, req.Cfg, req.WorkspaceHostPath, lastAppliedTemplates,
			oldSecretHashes, req.SecretHashes,
		)
		if err != nil {
			http.Error(w, fmt.Sprintf("diff: %v", err), http.StatusInternalServerError)
			return
		}

		// Partition into live, iron-proxy-restart, and teardown-required.
		var live, ironProxy, teardown []reconcile.Change
		for _, c := range changes {
			switch c.Bucket() {
			case reconcile.BucketLive:
				live = append(live, c)
			case reconcile.BucketEgressRestart:
				ironProxy = append(ironProxy, c)
			default:
				teardown = append(teardown, c)
			}
		}

		// Self-heal: a running VM whose iron-proxy is missing or stale
		// gets a synthetic KindIronProxyDown change appended, even when
		// the config diff itself is empty. This rides the existing
		// AppliedIronProxy path — the CLI already dispatches
		// /vm/apply-iron-proxy whenever AppliedIronProxy is non-empty.
		// Gated on running: a stopped VM has no live iron-proxy to heal.
		if running {
			if computeProxyHealth(sup, req.Name).Status != ProxyOK {
				ironProxy = append(ironProxy, reconcile.Change{Kind: reconcile.KindIronProxyDown})
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
				AppliedIronProxy: ironProxy,
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
			caPEM, err := os.ReadFile(filepath.Join(RuntimeDir(), "ca", "root.crt"))
			if err != nil {
				http.Error(w, fmt.Sprintf("read CA root: %v", err), http.StatusInternalServerError)
				return
			}
			if err := apply.ApplyLive(live, req.Cfg, req.WorkspaceHostPath, req.Name, caPEM, req.SSHAuthorizedPubkey, req.SSHHostPriv, req.SSHHostPub); err != nil {
				http.Error(w, fmt.Sprintf("apply live: %v", err), http.StatusInternalServerError)
				return
			}
			// Snapshot merge rule (§Decisions.9): merge only the
			// live-applied fields onto old_cfg so pending teardown
			// changes keep re-surfacing.
			merged := mergeLiveApplied(base, req.Cfg, live)
			// Recompute the template-contents baseline from the merged
			// cfg so it stays in lockstep with whatever templates that
			// cfg now declares (including any just applied live).
			mergedTemplates, err := render.RenderTemplatesByBasename(merged, req.WorkspaceHostPath)
			if err != nil {
				http.Error(w, fmt.Sprintf("render templates: %v", err), http.StatusInternalServerError)
				return
			}
			// projectIP/pickedSSHPort are read once here: they feed the
			// expose-map push and mirror through to the snapshot for
			// recoverProjectState.
			var projectIP string
			var pickedSSHPort int
			if info, ok := ironProxyState.get(req.Name); ok {
				projectIP = info.ProjectIP
				pickedSSHPort = info.PickedSSHPort
			}

			// Ingress: re-push softnet's expose map from the current cfg so
			// a live service/port change adds or drops host listeners.
			// Independent of egress policy. Pushed BEFORE the snapshot write
			// so a push failure leaves the baseline untouched — the handler's
			// "on failure, don't touch the snapshot" contract — and the
			// user's retry re-attempts the (idempotent, fully declarative)
			// push instead of silently skipping it against an advanced
			// baseline.
			if err := pushExposeMap(req.Name, computeExposeMap(req.Cfg, projectIP, pickedSSHPort)); err != nil {
				http.Error(w, fmt.Sprintf("push expose map: %v", err), http.StatusInternalServerError)
				return
			}
			if err := WriteStateSnapshot(req.Name, StateSnapshot{Cfg: merged, TemplateContents: mergedTemplates, SecretHashes: oldSecretHashes, ProjectIP: projectIP, PickedSSHPort: pickedSSHPort, WorkspaceHostPath: req.WorkspaceHostPath}); err != nil {
				http.Error(w, fmt.Sprintf("write state: %v", err), http.StatusInternalServerError)
				return
			}

		}

		// Config lock: after ANY reconcile on a running VM, re-establish
		// the "running VM ⟹ devm.yaml locked" invariant that /vm/start and
		// adopt set up — so an `unlock → edit → reconcile` cycle always ends
		// locked, whether or not the edit produced live changes. If the
		// project has flipped config_lock off, ensure it's unlocked instead.
		// Best-effort: a chflags failure must not fail a reconcile that
		// already succeeded; stopTimer cancels any pending relock timer from
		// the unlock this reconcile closes out. Only reached when running
		// (the !running path returned above).
		if req.WorkspaceHostPath != "" {
			if req.Cfg.ConfigLockEnabled() {
				if err := lockConfigFiles(req.WorkspaceHostPath); err != nil {
					debuglog.Logf("configlock", "re-lock config for %s: %v (continuing)", req.Name, err)
				} else {
					configLockState.put(req.Name, req.WorkspaceHostPath)
				}
				configLockState.stopTimer(req.Name)
			} else {
				if err := unlockConfigFiles(req.WorkspaceHostPath); err != nil {
					debuglog.Logf("configlock", "unlock config for %s (config_lock off): %v (continuing)", req.Name, err)
				}
				configLockState.del(req.Name)
			}
		}

		resp := VMReconcileResponse{
			Applied:          live,
			AppliedIronProxy: ironProxy,
			TeardownRequired: teardown,
			SandboxState:     state,
		}
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
	if len(applied) > 0 {
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
		case reconcile.KindServiceDirectChange:
			svc := merged.Services[c.Service]
			if newSvc, ok := new.Services[c.Service]; ok {
				svc.Direct = newSvc.Direct
				merged.Services[c.Service] = svc
			} else {
				delete(merged.Services, c.Service)
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
