package serviceapi

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/mdubb86/devm/internal/daemonlog"
	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/schema"
)

// VMSetAllowlistRequest is the body shape for POST /vm/set-allowlist.
// Sent by the CLI when reconcile detects a BucketLive
// KindNetworkAdd/KindNetworkRemove change: the full new allowlist is
// applied to the daemon's PolicyAuthority atomically — no iron-proxy
// touch, no denial-counter reset.
type VMSetAllowlistRequest struct {
	Name      string   `json:"name"`
	Allowlist []string `json:"allowlist"`
}

// RegisterSetAllowlistHandler wires POST /vm/set-allowlist. The
// project lock is acquired for the duration so a concurrent
// /vm/apply-iron-proxy or /vm/begin-provisioning can't race the
// authority Set + snapshot write.
func RegisterSetAllowlistHandler(s *Server, cfg identity.Config, locks *ProjectLocks) {
	s.Register("/vm/set-allowlist", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req VMSetAllowlistRequest
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

		policyAuthority.Set(req.Name, req.Allowlist)

		if err := updateSnapshotAfterAllowlistSet(cfg, req.Name, req.Allowlist); err != nil {
			daemonlog.Errorf("serviceapi: set-allowlist: update snapshot for %s: %v", req.Name, err)
			http.Error(w, fmt.Sprintf("update snapshot: %v", err), http.StatusInternalServerError)
			return
		}

		log.Printf("policy: set-allowlist applied for %s (%d hosts)", req.Name, len(req.Allowlist))

		w.WriteHeader(http.StatusNoContent)
	})
}

// updateSnapshotAfterAllowlistSet loads the current StateSnapshot for
// projectID and rebuilds snap.Cfg.Network.Allow from allowlist, then
// persists it. This is the same field recoverProjectState reads back
// via docker.EffectiveAllowlist(snap.Cfg) to re-Set the policy
// authority on daemon restart (ironproxy_discover.go), so a live Set
// here survives a daemon restart without a respawn.
//
// Existing per-host secret scope is preserved for hosts that remain in
// the new list — mirrors the allow-rebuild half of
// mergeAllowlistAndSecrets, but (unlike that function) touches nothing
// else in snap.Cfg: a plain allowlist Set is not a secret-binding
// change, so Env / Services[*].Env are left untouched.
//
// Requires a snapshot to already exist, same contract as
// updateSnapshotAfterSpawn: fabricating one here with a zero-valued
// Cfg would make every field in the eventual real cfg look like
// pending drift on the next reconcile.
func updateSnapshotAfterAllowlistSet(cfg identity.Config, projectID string, allowlist []string) error {
	snap, err := ReadStateSnapshot(cfg, projectID)
	if err != nil {
		return err
	}
	if snap == nil {
		return fmt.Errorf("set-allowlist called before /vm/start ever ran for project %q — snapshot not seeded", projectID)
	}

	oldByHost := make(map[string]schema.AllowEntry, len(snap.Cfg.Network.Allow))
	for _, e := range snap.Cfg.Network.Allow {
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
	snap.Cfg.Network.Allow = newAllow

	return WriteStateSnapshot(cfg, projectID, *snap)
}
