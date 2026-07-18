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

	"github.com/mdubb86/devm/internal/debuglog"
	"github.com/mdubb86/devm/internal/ironproxy"
	"github.com/mdubb86/devm/internal/sandbox/tart"
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
func RegisterApplyIronProxyHandler(s *Server, locks *ProjectLocks, sup *supervisor.Supervisor, tr *tart.Tart, denials *Denials) {
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
		cfgPath, err := IronProxyConfigPath(req.Name)
		if err != nil {
			http.Error(w, fmt.Sprintf("resolve config path: %v", err), http.StatusInternalServerError)
			return
		}
		info, err := loadIronProxyInfoFromConfig(cfgPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// No config file → the VM has never started an
				// iron-proxy for this project. Nothing to apply live,
				// but SecretHashes still needs to move forward so the
				// next /vm/start renders iron-proxy config from the
				// current schema without re-detecting this same drift.
				if err := updateSnapshotAfterSpawn(req.Name, hashes, false); err != nil {
					http.Error(w, fmt.Sprintf("update snapshot: %v", err), http.StatusInternalServerError)
					return
				}
				writeJSON(w, VMApplyIronProxyResponse{})
				return
			}
			http.Error(w, fmt.Sprintf("read iron-proxy config: %v", err), http.StatusInternalServerError)
			return
		}

		caDir, err := EnsureRuntimeDir()
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
			HTTPListen:  ironProxyListenAddr(info.HTTPPort),
			HTTPSListen: ironProxyListenAddr(info.HTTPSPort),
			DNSListen:   ironProxyListenAddr(info.DNSPort),
			DNSProxyIP:  proxySentinelIP,
			CACertPath:  filepath.Join(caDir, "ca", "root.crt"),
			CAKeyPath:   filepath.Join(caDir, "ca", "root.key"),
			AllowList:   req.Allowlist,
			Secrets:     secrets,
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

		if err := spawnIronProxyFn(r.Context(), sup, req.Name, newCfg, denials); err != nil {
			http.Error(w, fmt.Sprintf("spawn iron-proxy: %v", err), http.StatusInternalServerError)
			return
		}

		healthAddr := ironProxyListenAddr(info.HTTPSPort)
		if !waitIronProxyHealthy(healthAddr) {
			http.Error(w, fmt.Sprintf("iron-proxy spawned but did not bind %s within 2s", healthAddr),
				http.StatusInternalServerError)
			return
		}

		if err := updateSnapshotAfterSpawn(req.Name, hashes, true); err != nil {
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
		existing, _ := ironProxyState.get(req.Name)
		// SSHHostPort isn't part of iron-proxy's own YAML config shape
		// (loadIronProxyInfoFromConfig never sets it), so it must be
		// carried forward explicitly or this call would silently zero
		// out the running VM's SSH port on every allowlist/secret
		// reconcile — breaking the next expose-map push and any
		// already-emitted ssh_config.
		info.SSHHostPort = existing.SSHHostPort
		snap, _ := ReadStateSnapshot(req.Name)
		if snap != nil {
			info.SSHHostPort = snap.SSHHostPort
		}

		// Adopt-in-place (internal/orchestrator/shell.go's "pristine:
		// running but never provisioned" branch — raw `tart run`
		// adoption, or first-time adoption) calls this handler directly
		// and never /vm/start, so no SSH host port was ever allocated
		// for this project this daemon lifetime. Allocate one now so the
		// adopted VM converges to the same ingress state as a cold
		// start, instead of staying unreachable until an explicit stop +
		// restart. A non-zero port carried forward above (already
		// allocated by /vm/start, or by a prior call here) is left
		// as-is — reallocating would diverge from an already-emitted
		// ssh_config.
		if info.SSHHostPort == 0 {
			port, err := pickPort()
			if err != nil {
				http.Error(w, fmt.Sprintf("pick ssh host port: %v", err), http.StatusInternalServerError)
				return
			}
			info.SSHHostPort = port
		}
		ironProxyState.put(req.Name, info)

		// Push the ingress expose map from the project's persisted
		// config — the daemon's source of truth for an adopted VM,
		// which never sent a schema.Config in this request. Independent
		// of egress policy; non-fatal like /vm/start's push (vm.go)
		// because adopt-in-place must not fail just because ingress
		// couldn't be pushed (e.g. a cross-project port-claim
		// collision). Skipped when there's no persisted cfg yet —
		// nothing to expose.
		if snap != nil {
			if err := pushExposeMap(req.Name, computeExposeMap(snap.Cfg, info.SSHHostPort)); err != nil {
				debuglog.Logf("serviceapi", "apply-iron-proxy: push expose map for %s: %v", req.Name, err)
			}
		}

		writeJSON(w, VMApplyIronProxyResponse{
			Applied:   true,
			Revived:   !wasRunning,
			VMRunning: true,
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
// projectID, overwrites SecretHashes, and persists it — preserving Cfg
// and TemplateContents as-is. Used on every success path — including
// the VM-stopped no-op — so the daemon's drift baseline always
// reflects the secrets the CLI most recently resolved from the
// keychain.
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
func updateSnapshotAfterSpawn(projectID string, hashes map[string]string, stampVersion bool) error {
	snap, err := ReadStateSnapshot(projectID)
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
	return WriteStateSnapshot(projectID, *snap)
}

// writeJSON writes body as JSON with 200 OK.
func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
}
