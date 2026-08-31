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
	"strings"
	"time"

	"github.com/mdubb86/devm/internal/daemonlog"
	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/mutagen"
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
//
// identCfg and ironProxyURL feed reconcile.ApplyLive's mutagen-session
// branch (KindRepoChange/KindVolumeChange): identCfg scopes mirror
// paths and the mutagen data dir to this daemon's identity;
// ironProxyURL is this project's current iron-proxy CONNECT URL, used
// only when the change implies a cold-start clone.
type ApplyLiver interface {
	ApplyLive(changes []reconcile.Change, cfg schema.Config, repoRoot, daemonRuntimeDir, vmName string, caPEM, sshAuthPub, sshHostPriv, sshHostPub []byte, identCfg identity.Config, ironProxyURL string) error
}

// realApplyLiver adapts reconcile.ApplyLive to the interface.
type realApplyLiver struct{ tr *tart.Tart }

func (r *realApplyLiver) ApplyLive(changes []reconcile.Change, cfg schema.Config, repoRoot, daemonRuntimeDir, vmName string, caPEM, sshAuthPub, sshHostPriv, sshHostPub []byte, identCfg identity.Config, ironProxyURL string) error {
	mutagenBin, err := mutagen.Ensure(identCfg.RuntimeDir())
	if err != nil {
		return fmt.Errorf("apply live: mutagen: extract binary: %w", err)
	}
	mutagenCLI := &mutagen.CLI{Binary: mutagenBin, DataDir: mutagenDataDir(identCfg), Exec: mutagen.OSExec}
	// A KindNetworkAdd/Remove change dispatches through the
	// AllowlistSetter reconcile.ApplyLive is given. reconcile.ApplyLive
	// can't construct one itself: internal/reconcile can't import
	// internal/serviceapi (this package already imports reconcile), so
	// AllowlistSetter is declared there as a local interface and
	// satisfied here by inProcessAllowlistSetter.
	return reconcile.ApplyLive(r.tr, vmName, changes, cfg, repoRoot, daemonRuntimeDir, caPEM, sshAuthPub, sshHostPriv, sshHostPub, mutagenCLI, identCfg, ironProxyURL, &inProcessAllowlistSetter{cfg: identCfg})
}

// inProcessAllowlistSetter satisfies reconcile.AllowlistSetter for
// reconcile-triggered allowlist writes. The /vm/reconcile handler holds
// req.Name's project lock across the ApplyLive call this setter is
// reached from, so SetAllowlist here does NOT re-acquire it — it writes
// policyAuthority + snapshot directly. This is the only production
// implementer of reconcile.AllowlistSetter.
type inProcessAllowlistSetter struct {
	cfg identity.Config
}

func (s *inProcessAllowlistSetter) SetAllowlist(ctx context.Context, name string, allowlist []string) error {
	policyAuthority.SetAllowlist(name, allowlist)
	return updateSnapshotAfterAllowlistSet(s.cfg, name, allowlist)
}

// TartLister is the subset of *tart.Tart the reconcile handler uses to
// check VM running state before deciding whether to apply live changes.
type TartLister interface {
	List(ctx context.Context) ([]tart.VM, error)
}

// reconcileHandler returns an http.Handler for POST /vm/reconcile.
func reconcileHandler(cfg identity.Config, locks *ProjectLocks, apply ApplyLiver, packages PackagesApplier, tr TartLister, sup *supervisor.Supervisor, proxy *ProxyServer, ntpPort int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

		// Approve-gate check: refuse if diverged from approved snapshot.
		if diverged, err := isApproveDiverged(cfg, req.Name, req.WorkspaceHostPath); err != nil {
			http.Error(w, fmt.Sprintf("approve check: %v", err), http.StatusInternalServerError)
			return
		} else if diverged {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"code":    "approve_required",
				"message": approveRefusalMessage,
			})
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
		oldSnap, err := ReadStateSnapshot(cfg, req.Name)
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
			base, req.Cfg, req.WorkspaceHostPath, cfg.RuntimeDir(), lastAppliedTemplates,
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
			if computeProxyHealth(cfg, sup, proxy, req.Name).Status != ProxyOK {
				ironProxy = append(ironProxy, reconcile.Change{Kind: reconcile.KindIronProxyDown})
			}
		}

		// SSH-endpoint verify + heal: a running project's :22 must be
		// answered by its own guest sshd. A foreign host key means the
		// ProjectIP is cross-wired — a listener outside daemon state
		// (typically an orphaned VM's softnet) owns the bind — so move
		// the project to a fresh IP. Heal only on a definitive key
		// MISMATCH: a failed or non-SSH handshake can be a booting
		// guest or a wedged sshd, where reallocating would churn IPs
		// for nothing — those log loudly and stay put. Runs before the
		// live-apply section so its expose push targets the healed IP.
		var sshHealed []reconcile.Change
		if running && len(req.SSHHostPub) > 0 {
			if info, ok := ironProxyState.get(req.Name); ok && info.ProjectIP != "" {
				oldIP := info.ProjectIP
				verr := verifySSHHostKey(sshVerifyAddr(oldIP), req.SSHHostPub, 4*time.Second)
				switch {
				case verr == nil:
					// healthy
				case strings.Contains(verr.Error(), "host key mismatch"):
					daemonlog.Errorf("serviceapi: reconcile: %s is cross-wired (%v) — reallocating", req.Name, verr)
					newIP, err := healCrossWiredIP(r.Context(), cfg, req.Name, req.Cfg, proxy, ntpPort)
					if err != nil {
						http.Error(w, fmt.Sprintf("ssh endpoint cross-wired (%v) and heal failed: %v", verr, err), http.StatusInternalServerError)
						return
					}
					if verr2 := verifySSHHostKey(sshVerifyAddr(newIP), req.SSHHostPub, 4*time.Second); verr2 != nil {
						http.Error(w, fmt.Sprintf("ssh endpoint still unhealthy after heal to %s: %v", newIP, verr2), http.StatusInternalServerError)
						return
					}
					sshHealed = append(sshHealed, reconcile.Change{Kind: reconcile.KindSSHEndpointHealed, Old: oldIP, New: newIP})
				default:
					daemonlog.Errorf("serviceapi: reconcile: ssh endpoint check for %s inconclusive (not healing): %v", req.Name, verr)
				}
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
			caPEM, err := os.ReadFile(filepath.Join(cfg.RuntimeDir(), "ca", "root.crt"))
			if err != nil {
				http.Error(w, fmt.Sprintf("read CA root: %v", err), http.StatusInternalServerError)
				return
			}
			// Packages converge first: a template installer applied in the
			// same reconcile may invoke binaries the new packages provide.
			// Package changes stay in `live` for the snapshot merge below;
			// ApplyLive has no case for them and skips them.
			//
			// snapCfg is `base` (the OLD snapshot cfg), not req.Cfg: the
			// applier rebuilds and restores the iron-proxy config the
			// running proxy currently reflects — the last-applied state.
			// Pending egress changes computed in this same reconcile are
			// applied afterwards by the CLI's apply-iron-proxy dispatch
			// (AppliedIronProxy), not here.
			var pkgAdds, pkgRemoves []string
			for _, c := range live {
				switch c.Kind {
				case reconcile.KindPackageAdd:
					pkgAdds = append(pkgAdds, c.Key)
				case reconcile.KindPackageRemove:
					pkgRemoves = append(pkgRemoves, c.Key)
				}
			}
			if len(pkgAdds)+len(pkgRemoves) > 0 {
				if err := packages.ApplyPackages(r.Context(), req.Name, base, req.WorkspaceHostPath, pkgAdds, pkgRemoves); err != nil {
					http.Error(w, fmt.Sprintf("apply packages: %v", err), http.StatusInternalServerError)
					return
				}
			}
			if err := apply.ApplyLive(live, req.Cfg, req.WorkspaceHostPath, cfg.RuntimeDir(), req.Name, caPEM, req.SSHAuthorizedPubkey, req.SSHHostPriv, req.SSHHostPub, cfg, ironProxyURLFor(req.Name)); err != nil {
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
			mergedTemplates, err := render.RenderTemplatesByBasename(merged, req.WorkspaceHostPath, cfg.RuntimeDir(), req.Name)
			if err != nil {
				http.Error(w, fmt.Sprintf("render templates: %v", err), http.StatusInternalServerError)
				return
			}
			// projectIP is read once here: it feeds the expose-map push
			// and mirrors through to the snapshot for recoverProjectState.
			var projectIP string
			if info, ok := ironProxyState.get(req.Name); ok {
				projectIP = info.ProjectIP
			}

			// Ingress: re-push softnet's expose map from the current cfg so
			// a live service/port change adds or drops host listeners.
			// Independent of egress policy. Pushed BEFORE the snapshot write
			// so a push failure leaves the baseline untouched — the handler's
			// "on failure, don't touch the snapshot" contract — and the
			// user's retry re-attempts the (idempotent, fully declarative)
			// push instead of silently skipping it against an advanced
			// baseline.
			if err := pushExposeMap(req.Name, computeExposeMap(req.Cfg, projectIP)); err != nil {
				http.Error(w, fmt.Sprintf("push expose map: %v", err), http.StatusInternalServerError)
				return
			}
			if err := pushTestHosts(req.Name, computeDirectTestHosts(req.Cfg)); err != nil {
				http.Error(w, fmt.Sprintf("push test hosts: %v", err), http.StatusInternalServerError)
				return
			}
			if err := WriteStateSnapshot(cfg, req.Name, StateSnapshot{Cfg: merged, TemplateContents: mergedTemplates, SecretHashes: oldSecretHashes, ProjectIP: projectIP, MacCwd: req.WorkspaceHostPath}); err != nil {
				http.Error(w, fmt.Sprintf("write state: %v", err), http.StatusInternalServerError)
				return
			}

		}

		resp := VMReconcileResponse{
			Applied:          append(live, sshHealed...),
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

// RegisterReconcileHandler wires POST /vm/reconcile. sup is consulted
// (only when the VM is running) to self-heal a missing/stale
// iron-proxy: see the KindIronProxyDown emit below.
func RegisterReconcileHandler(s *Server, cfg identity.Config, locks *ProjectLocks, apply ApplyLiver, packages PackagesApplier, tr TartLister, sup *supervisor.Supervisor, proxy *ProxyServer, ntpPort int) {
	handler := reconcileHandler(cfg, locks, apply, packages, tr, sup, proxy, ntpPort)
	s.Register("/vm/reconcile", handler.(http.HandlerFunc))
}

// mergeLiveApplied returns a cfg that equals old_cfg except in the
// exact fields represented by the applied Change list — those come
// from new_cfg. Pending teardown-required fields on the same service
// (or elsewhere) MUST stay at their old_cfg values so they keep
// re-surfacing on subsequent reconciles.
//
// Granularity: env is by-key (top-level or per-service); path is
// wholesale; network by-list-membership; per-service subfield changes
// touch ONLY that subfield on the service; repos and commands (both
// live inside Config.Repos) and volumes are wholesale on their
// respective top-level map.
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
		case reconcile.KindRepoChange, reconcile.KindCommandsChange:
			// Both live inside Config.Repos; wholesale replace covers both.
			merged.Repos = new.Repos
		case reconcile.KindVolumeChange:
			merged.Volumes = new.Volumes
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
		case reconcile.KindPackageAdd, reconcile.KindPackageRemove:
			merged.Packages = new.Packages
		}
	}
	return merged
}
