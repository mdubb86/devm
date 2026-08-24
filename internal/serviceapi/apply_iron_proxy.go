package serviceapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/mdubb86/devm/internal/daemonlog"
	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/ironproxy"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/supervisor"
)

// VMApplyIronProxyRequest is the body shape for POST /vm/apply-iron-proxy.
// Sent by the CLI when reconcile detects BucketEgressRestart changes
// (allow-list or secret-binding drift that requires a fresh iron-proxy
// config + process, but doesn't touch the VM itself).
type VMApplyIronProxyRequest struct {
	Name      string          `json:"name"`
	Allowlist []string        `json:"allowlist,omitempty"`
	Secrets   []SecretBinding `json:"secrets,omitempty"`
}

// VMApplyIronProxyResponse is the return shape.
//
//	Applied   -- true when a fresh iron-proxy config was written AND
//	             iron-proxy was spawned and verified healthy (running
//	             or revived case).
//	Revived   -- true only when iron-proxy was previously dead but the
//	             config file existed; a fresh spawn recovered it.
//	VMRunning -- false when no config file exists (VM has never
//	             started iron-proxy for this project). The daemon's
//	             stored SecretHashes still advance in that case so a
//	             future /vm/start doesn't re-detect the same drift.
type VMApplyIronProxyResponse struct {
	Applied   bool `json:"applied"`
	Revived   bool `json:"revived"`
	VMRunning bool `json:"vm_running"`
	// ProjectIP is the project's allocated 127.42/16 loopback IP,
	// returned so the CLI can seed it into a state snapshot for
	// adopt-in-place — mirrors VMStartResponse.ProjectIP for the
	// cold-start path. Empty in the VM-never-started (VMRunning=false)
	// case, since no IP is allocated there.
	ProjectIP string `json:"project_ip,omitempty"`
}

// spawnIronProxyFn is the test-injection seam for SpawnIronProxy.
// Production code always uses SpawnIronProxy itself; tests substitute a
// stub so they don't have to exec the real (expensive) iron-proxy
// binary just to exercise the handler's control flow.
var spawnIronProxyFn = SpawnIronProxy

// Health-verify budget for a freshly spawned iron-proxy: 20 attempts at
// 100ms apart, ~2s worst case. iron-proxy has no dedicated healthcheck
// endpoint, so a successful TCP connect to its HTTPS listener is the
// cheapest reliable "it's alive" signal available.
const (
	ironProxyHealthAttempts = 20
	ironProxyHealthInterval = 100 * time.Millisecond
)

// RegisterApplyIronProxyHandler wires POST /vm/apply-iron-proxy. The
// project lock is acquired for the duration; concurrent starts, stops,
// and reconciles for the same project can't race with it.
//
// Fail-loud contract: any failure spawning iron-proxy, verifying its
// health, or persisting the snapshot returns 500 and leaves the
// snapshot untouched (except the two success/no-op paths, which
// deliberately advance SecretHashes).
func RegisterApplyIronProxyHandler(s *Server, cfg identity.Config, locks *ProjectLocks, sup *supervisor.Supervisor, tr *tart.Tart, denials *Denials, proxy *ProxyServer) {
	s.Register("/vm/apply-iron-proxy", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req VMApplyIronProxyRequest
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

		hashes := secretHashesFromBindings(req.Secrets)

		// Read the existing iron-proxy config for ports + MAC_HOST. The
		// dnsmasq inside the guest is already pointing at these ports;
		// we must preserve them or DNS silently breaks.
		cfgPath, err := IronProxyConfigPath(cfg, req.Name)
		if err != nil {
			http.Error(w, fmt.Sprintf("resolve config path: %v", err), http.StatusInternalServerError)
			return
		}
		diskInfo, err := loadIronProxyInfoFromConfig(cfgPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// No config file → the VM has never started an
				// iron-proxy for this project. Nothing to apply live,
				// but SecretHashes and snap.Cfg still need to move
				// forward so the next /vm/start renders iron-proxy
				// config from the current schema without re-detecting
				// this same drift.
				if err := updateSnapshotAfterSpawn(cfg, req.Name, hashes, false, req.Allowlist, req.Secrets); err != nil {
					http.Error(w, fmt.Sprintf("update snapshot: %v", err), http.StatusInternalServerError)
					return
				}
				writeJSON(w, VMApplyIronProxyResponse{})
				return
			}
			http.Error(w, fmt.Sprintf("read iron-proxy config: %v", err), http.StatusInternalServerError)
			return
		}

		caDir, err := EnsureRuntimeDir(cfg)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		secrets := make([]IronSecret, 0, len(req.Secrets))
		for _, sb := range req.Secrets {
			secrets = append(secrets, IronSecret{Name: sb.Name, Value: sb.Value, Hosts: sb.Hosts})
		}
		// Build fresh config on the SAME MAC_HOST + ports pulled from
		// the on-disk config above.
		newCfg := IronProxyConfig{
			HTTPListen:   ironProxyListenAddr(diskInfo.HTTPPort),
			HTTPSListen:  ironProxyListenAddr(diskInfo.HTTPSPort),
			TunnelListen: ironProxyListenAddr(diskInfo.TunnelPort),
			DNSListen:    ironProxyListenAddr(diskInfo.DNSPort),
			DNSProxyIP:   interceptedEgressIP,
			CACertPath:   filepath.Join(caDir, "ca", "root.crt"),
			CAKeyPath:    filepath.Join(caDir, "ca", "root.key"),
			AllowList:    req.Allowlist,
			Secrets:      secrets,
		}

		// Is iron-proxy alive for this project right now? Determines
		// Revived in the response: config existed on disk, but no live
		// process, means this spawn is a revival rather than a restart.
		key := supervisor.Key{ProjectID: req.Name, Role: supervisor.RoleProxy}
		wasRunning := sup.Status(key).Present && sup.Status(key).Running

		if wasRunning {
			// supervisor.Spawn (via AddProcess) silently replaces the
			// registry entry for this key without stopping the prior
			// process, so the old iron-proxy must be stopped explicitly
			// or it leaks as an orphan holding the old ports.
			if err := sup.Stop(r.Context(), key); err != nil && !errors.Is(err, supervisor.ErrNotFound) {
				http.Error(w, fmt.Sprintf("stop iron-proxy: %v", err), http.StatusInternalServerError)
				return
			}
		}

		if err := spawnIronProxyFn(r.Context(), cfg, sup, req.Name, newCfg, denials); err != nil {
			http.Error(w, fmt.Sprintf("spawn iron-proxy: %v", err), http.StatusInternalServerError)
			return
		}

		healthAddr := ironProxyListenAddr(diskInfo.HTTPSPort)
		if !waitIronProxyHealthy(healthAddr) {
			http.Error(w, fmt.Sprintf("iron-proxy spawned but did not bind %s within 2s", healthAddr),
				http.StatusInternalServerError)
			return
		}

		if err := updateSnapshotAfterSpawn(cfg, req.Name, hashes, true, req.Allowlist, req.Secrets); err != nil {
			http.Error(w, fmt.Sprintf("update snapshot: %v", err), http.StatusInternalServerError)
			return
		}

		// Rehydrate ironProxyState from the same on-disk config so
		// /vm/enforcement-config keeps working for this project — it
		// reads MAC_HOST/ports/Docker from ironProxyState, not from
		// disk. Without this, a caller that reaches this handler with
		// an empty ironProxyState (the VM's own process was never
		// (re)started here, e.g. adopt-in-place after `devm stop`
		// tore the previous iron-proxy down with it) would spawn a
		// healthy iron-proxy yet still 412 on the very next
		// EnforcementConfig fetch. Mirrors AdoptIronProxies'
		// daemon-restart rehydration (ironproxy_discover.go).
		//
		// Merge onto the existing registry entry rather than overwrite
		// it with diskInfo — iron-proxy's on-disk YAML only carries
		// HTTP/HTTPS/DNS ports, not ProjectIP or the guest-origin
		// listener pair, both of which live only in the registry. Start
		// from the existing entry and overlay just the fields diskInfo
		// actually knows, mirroring /vm/start's own merge (vm.go): a
		// field added to projectInfo later is preserved by default
		// instead of needing its own carry-forward line here.
		info, _ := ironProxyState.get(req.Name)
		info.HTTPPort = diskInfo.HTTPPort
		info.HTTPSPort = diskInfo.HTTPSPort
		info.TunnelPort = diskInfo.TunnelPort
		info.DNSPort = diskInfo.DNSPort
		ironProxyState.put(req.Name, info)

		snap, _ := ReadStateSnapshot(cfg, req.Name)

		// Adopt-in-place (internal/orchestrator/shell.go's "pristine:
		// running but never provisioned" branch — raw `tart run`
		// adoption, or first-time adoption) calls this handler directly
		// and never /vm/start, so no project IP was ever allocated for
		// this project this daemon lifetime. AllocateProjectIP is
		// idempotent — it returns the existing IP untouched when
		// /vm/start or a prior call here already allocated one — so the
		// adopted VM converges to the same ingress state as a cold
		// start, instead of staying unreachable until an explicit stop +
		// restart. Called after the put above so AllocateProjectIP's own
		// merge-onto-existing-entry logic (projectip.go) has the
		// HTTP/HTTPS/DNS ports already in place to merge onto.
		projectIP, err := AllocateProjectIP(cfg, req.Name)
		if err != nil {
			http.Error(w, fmt.Sprintf("allocate project ip: %v", err), http.StatusInternalServerError)
			return
		}

		// Adopt-in-place never calls /vm/start, so this project's
		// guest-origin listener pair may never have been started this
		// daemon lifetime — nothing is listening on the other end of
		// softnet's `.test` hairpin. Start it here too, mirroring
		// /vm/start (vm.go): idempotent (a live pair is left untouched
		// and its existing ports returned), and its ports are recorded
		// in the registry before any endpoint push can read them. proxy
		// is nil only in tests that don't exercise this path; production
		// always wires one (runner.go). This deliberately does not call
		// StartProjectListeners — the browser-facing :80/:443 bind is
		// Mac-side and out of scope here.
		if proxy != nil {
			guestHTTPPort, guestHTTPSPort, gerr := proxy.StartGuestOriginListeners(r.Context(), req.Name, projectIP)
			if gerr != nil {
				http.Error(w, fmt.Sprintf("start guest-origin listeners: %v", gerr), http.StatusInternalServerError)
				return
			}
			info, _ = ironProxyState.get(req.Name)
			info.GuestHTTPPort = guestHTTPPort
			info.GuestHTTPSPort = guestHTTPSPort
			ironProxyState.put(req.Name, info)
		}

		// Adopt-in-place also never went through /vm/start's
		// softnetState.put (vm.go), so the daemon's in-memory
		// projectID -> softnet-control-socket map has no entry for
		// this project either — /vm/apply-egress-enforcement and
		// /vm/open-egress 412 ("softnet control socket missing") on
		// the very next call, and the expose-map push below silently
		// no-ops instead of actually reaching softnet (see
		// pushExposeMap's softnetState.get check in expose.go).
		// SoftnetControlSock is a pure function of
		// (cfg.RuntimeDir(), projectID) — deterministic, no
		// filesystem/process lookup needed — so this re-registration
		// mirrors exactly what /vm/start (vm.go) and discoverSoftnet
		// (the daemon-restart rehydration path) already do to
		// populate the same map.
		softnetState.put(req.Name, SoftnetControlSock(cfg, req.Name))

		// Push the ingress expose map from the project's persisted
		// config — the daemon's source of truth for an adopted VM,
		// which never sent a schema.Config in this request. Independent
		// of egress policy; non-fatal like /vm/start's push (vm.go)
		// because adopt-in-place must not fail just because ingress
		// couldn't be pushed (e.g. a cross-project port-claim
		// collision). Skipped when there's no persisted cfg yet —
		// nothing to expose.
		if snap != nil {
			if err := pushExposeMap(req.Name, computeExposeMap(snap.Cfg, projectIP)); err != nil {
				daemonlog.Errorf("serviceapi: apply-iron-proxy: push expose map for %s: %v", req.Name, err)
			}
			if err := pushTestHosts(req.Name, computeDirectTestHosts(snap.Cfg)); err != nil {
				daemonlog.Errorf("serviceapi: apply-iron-proxy: push test hosts for %s: %v", req.Name, err)
			}
		}

		writeJSON(w, VMApplyIronProxyResponse{
			Applied:   true,
			Revived:   !wasRunning,
			VMRunning: true,
			ProjectIP: projectIP,
		})
	})
}

// waitIronProxyHealthy polls addr with a short TCP dial until it
// accepts connections or the attempt budget is exhausted.
func waitIronProxyHealthy(addr string) bool {
	for i := 0; i < ironProxyHealthAttempts; i++ {
		conn, err := net.DialTimeout("tcp", addr, ironProxyHealthInterval)
		if err == nil {
			_ = conn.Close()
			return true
		}
		if i < ironProxyHealthAttempts-1 {
			time.Sleep(ironProxyHealthInterval)
		}
	}
	return false
}

// secretHashesFromBindings returns a {Name: hex(sha256(Value))} map for
// the given resolved secret bindings. Mirrors
// orchestrator.SecretHashesFromBindings; duplicated here rather than
// imported because internal/orchestrator already imports serviceapi,
// so importing it back would be a cycle.
//
// Empty / nil input yields nil so the map is trivially JSON-omitted.
func secretHashesFromBindings(bindings []SecretBinding) map[string]string {
	if len(bindings) == 0 {
		return nil
	}
	out := make(map[string]string, len(bindings))
	for _, b := range bindings {
		sum := sha256.Sum256([]byte(b.Value))
		out[b.Name] = hex.EncodeToString(sum[:])
	}
	return out
}

// updateSnapshotAfterSpawn loads the current StateSnapshot for
// projectID, folds the just-applied allowlist + secret bindings into
// snap.Cfg via mergeAllowlistAndSecrets, updates SecretHashes /
// ProxyVersion, and persists it. Advancing snap.Cfg here is what makes
// subsequent reconciles diff against the just-applied state instead of
// a stale baseline — without it, allow-list and !secret-ref removals
// are silently no-op'd on the next reconcile.
//
// When stampVersion is true (a spawn actually happened), also sets
// ProxyVersion = ironproxy.EmbeddedSha256() so a later STALE check can
// tell this proxy was (re)spawned on the current devm build. The
// VM-stopped no-op path passes false: no proxy was touched, so its
// version stamp — whatever it is — must not change.
//
// Requires a snapshot to already exist: cold-start (`devm start` /
// `devm shell`) seeds one with the real schema.Config before
// apply-iron-proxy can ever be called. If none exists, fabricating one
// here with a zero-valued Cfg would make every real field in the
// eventual cold-start cfg look like a pending change on the next
// reconcile — a teardown-required storm. Fail loud instead and leave
// the (nonexistent) snapshot untouched.
func updateSnapshotAfterSpawn(
	cfg identity.Config,
	projectID string,
	hashes map[string]string,
	stampVersion bool,
	appliedAllowlist []string,
	appliedSecrets []SecretBinding,
) error {
	snap, err := ReadStateSnapshot(cfg, projectID)
	if err != nil {
		return err
	}
	if snap == nil {
		return fmt.Errorf("apply-iron-proxy called before /vm/start ever ran for project %q — snapshot not seeded", projectID)
	}
	snap.SecretHashes = hashes
	if stampVersion {
		snap.ProxyVersion = ironproxy.EmbeddedSha256()
	}
	snap.Cfg = mergeAllowlistAndSecrets(snap.Cfg, appliedAllowlist, appliedSecrets)
	return WriteStateSnapshot(cfg, projectID, *snap)
}

// writeJSON writes body as JSON with 200 OK.
func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
}

// mergeAllowlistAndSecrets returns snapCfg with Network.Allow rebuilt
// from allowlist and secret refs in Env / Services[*].Env cleared when
// their referenced secret name is no longer in secrets. All other Cfg
// fields are preserved — apply-iron-proxy's scope is egress policy only.
//
// Per-host secret scope: each schema.AllowEntry carries {Host, Secrets
// []string}. allowlist is []string, so per-host scope isn't in the
// input. Preserve it by copying from the current snapCfg.Network.Allow
// when a host matches. New hosts get empty scope (no per-host secret
// binding). A per-host-scope-only change (same hosts, different
// per-host secret binding) is a pre-existing diff-engine blind spot —
// computeNetworkChanges diffs Domains() only, not scope, so scope-only
// changes never trigger BucketEgressRestart and never reach this
// merge. Track separately if that gap needs closing.
//
// Secret refs: for each EnvValue whose Secret != nil, drop the ref
// (leaving a zero-value EnvValue) if the referenced name is not in
// secrets. A later live-bucket reconcile will surface the actual
// replacement (literal or removal), which mergeLiveApplied then
// applies. This keeps the snapshot from LYING about an active secret
// binding that's been removed.
func mergeAllowlistAndSecrets(snapCfg schema.Config, allowlist []string, secrets []SecretBinding) schema.Config {
	// Index existing allow entries by host so we can preserve per-host
	// secret scope on rebuild.
	oldByHost := make(map[string]schema.AllowEntry, len(snapCfg.Network.Allow))
	for _, e := range snapCfg.Network.Allow {
		oldByHost[e.Host] = e
	}
	newAllow := make([]schema.AllowEntry, 0, len(allowlist))
	for _, host := range allowlist {
		if prev, ok := oldByHost[host]; ok {
			newAllow = append(newAllow, prev)
			continue
		}
		newAllow = append(newAllow, schema.AllowEntry{Host: host})
	}
	snapCfg.Network.Allow = newAllow

	// Set of currently-bound secret names.
	bound := make(map[string]struct{}, len(secrets))
	for _, s := range secrets {
		bound[s.Name] = struct{}{}
	}

	// Clear any secret refs no longer bound. Global Env.
	if len(snapCfg.Env) > 0 {
		newEnv := make(map[string]schema.EnvValue, len(snapCfg.Env))
		for k, v := range snapCfg.Env {
			if v.IsSecret() {
				if _, ok := bound[v.Secret.Name]; !ok {
					continue // drop the key — literal replacement (if any) surfaces via a subsequent Live reconcile
				}
			}
			newEnv[k] = v
		}
		snapCfg.Env = newEnv
	}

	// Per-service Env. Copy the services map before mutating so we don't
	// alias the input.
	if len(snapCfg.Services) > 0 {
		newServices := make(map[string]schema.Service, len(snapCfg.Services))
		for name, svc := range snapCfg.Services {
			if len(svc.Env) > 0 {
				newSvcEnv := make(map[string]schema.EnvValue, len(svc.Env))
				for k, v := range svc.Env {
					if v.IsSecret() {
						if _, ok := bound[v.Secret.Name]; !ok {
							continue
						}
					}
					newSvcEnv[k] = v
				}
				svc.Env = newSvcEnv
			}
			newServices[name] = svc
		}
		snapCfg.Services = newServices
	}

	return snapCfg
}
