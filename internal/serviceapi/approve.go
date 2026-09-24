// approve-gate HTTP handlers. See
// docs/superpowers/specs/2026-08-31-devm-approve-gate-design.md.
package serviceapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/mdubb86/devm/internal/approve"
	"github.com/mdubb86/devm/internal/daemonlog"
	"github.com/mdubb86/devm/internal/identity"
)

// projectConfigPath returns <macCwd>/<kind> for project name, where
// kind is "devm.yaml" or "devm.me.yaml". Returns "" when the project
// isn't in the cache or has no MacCwd yet — callers must treat that
// as "project not started".
func projectConfigPath(cache *StateCache, name, kind string) string {
	row, ok := cache.ProjectRow(name)
	if !ok || row.MacCwd == "" {
		return ""
	}
	return filepath.Join(row.MacCwd, kind)
}

// stateDirForProject returns <RuntimeDir>/<name>, home to the approved-snapshot dir and last-proposal.json.
func stateDirForProject(cfg identity.Config, name string) string {
	return filepath.Join(cfg.RuntimeDir(), name)
}

// resolveMacCwdForRead picks a project's Mac cwd for reading devm.yaml
// / devm.me.yaml. Prefers the state cache (set by /vm/start and
// persisted across daemon restarts). Falls back to a caller-supplied
// cwd query param — used by CLI verbs like approve that can be run on
// a project the daemon doesn't currently track (e.g. after
// `devm teardown --yes` clears the cache entry). Returns "" only when
// neither source is available.
func resolveMacCwdForRead(cache *StateCache, project, reqCwd string) string {
	if cache != nil {
		if row, ok := cache.ProjectRow(project); ok && row.MacCwd != "" {
			return row.MacCwd
		}
	}
	return reqCwd
}

type approveStateResponse struct {
	Project               string            `json:"project"`
	Diverged              bool              `json:"diverged"`
	CurrentDevmSHA        string            `json:"current_devm_sha"`
	ApprovedDevmSHA       string            `json:"approved_devm_sha"`
	CurrentMeSHA          string            `json:"current_me_sha"`
	ApprovedMeSHA         string            `json:"approved_me_sha"`
	CurrentScriptSHA      string            `json:"current_script_sha"`
	ApprovedScriptSHA     string            `json:"approved_script_sha"`
	CurrentMeScriptSHA    string            `json:"current_me_script_sha"`
	ApprovedMeScriptSHA   string            `json:"approved_me_script_sha"`
	CurrentDevmBytes      string            `json:"current_devm_bytes"`
	ApprovedDevmBytes     *string           `json:"approved_devm_bytes"`
	CurrentMeBytes        *string           `json:"current_me_bytes"`
	ApprovedMeBytes       *string           `json:"approved_me_bytes"`
	CurrentScriptBytes    *string           `json:"current_script_bytes"`
	ApprovedScriptBytes   *string           `json:"approved_script_bytes"`
	CurrentMeScriptBytes  *string           `json:"current_me_script_bytes"`
	ApprovedMeScriptBytes *string           `json:"approved_me_script_bytes"`
	ApprovedSince         *string           `json:"approved_since"`
	ApprovedSource        *string           `json:"approved_source"`
	Proposal              *ProposalMetadata `json:"proposal"`
}

func handleApproveState(cfg identity.Config, cache *StateCache) http.Handler {
	fn := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "approve-state: GET only", http.StatusMethodNotAllowed)
			return
		}
		project := r.URL.Query().Get("project")
		if project == "" {
			http.Error(w, "approve-state: project query param required", http.StatusBadRequest)
			return
		}
		macCwd := resolveMacCwdForRead(cache, project, r.URL.Query().Get("cwd"))
		if macCwd == "" {
			// No MacCwd from cache OR request — nothing to compare
			// against. Return an empty response (not-diverged) rather
			// than surfacing an error; this preserves the older
			// "silent no-op" behavior for callers that don't yet pass cwd.
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(approveStateResponse{Project: project})
			return
		}
		devmPath := filepath.Join(macCwd, "devm.yaml")
		currentDevm, err := os.ReadFile(devmPath)
		if err != nil {
			http.Error(w, fmt.Sprintf("approve-state: read devm.yaml: %v", err), http.StatusInternalServerError)
			return
		}
		var currentMe []byte
		if b, err := os.ReadFile(filepath.Join(macCwd, "devm.me.yaml")); err == nil {
			currentMe = b
		} else if !errors.Is(err, os.ErrNotExist) {
			http.Error(w, fmt.Sprintf("approve-state: read devm.me.yaml: %v", err), http.StatusInternalServerError)
			return
		}
		var currentScript []byte
		if b, err := os.ReadFile(filepath.Join(macCwd, "devm.sh")); err == nil {
			currentScript = b
		} else if !errors.Is(err, os.ErrNotExist) {
			http.Error(w, fmt.Sprintf("approve-state: read devm.sh: %v", err), http.StatusInternalServerError)
			return
		}
		var currentMeScript []byte
		if b, err := os.ReadFile(filepath.Join(macCwd, "devm.me.sh")); err == nil {
			currentMeScript = b
		} else if !errors.Is(err, os.ErrNotExist) {
			http.Error(w, fmt.Sprintf("approve-state: read devm.me.sh: %v", err), http.StatusInternalServerError)
			return
		}
		store := approve.NewStore(cfg)
		snap, hasSnap, err := store.Read(project)
		if err != nil {
			http.Error(w, fmt.Sprintf("approve-state: read snapshot: %v", err), http.StatusInternalServerError)
			return
		}
		curDevmSHA := approve.HashFile(currentDevm)
		curMeSHA := approve.HashFile(currentMe)
		curScriptSHA := approve.HashFile(currentScript)
		curMeScriptSHA := approve.HashFile(currentMeScript)
		resp := approveStateResponse{
			Project:             project,
			CurrentDevmSHA:      curDevmSHA,
			CurrentMeSHA:        curMeSHA,
			CurrentScriptSHA:    curScriptSHA,
			CurrentMeScriptSHA:  curMeScriptSHA,
			CurrentDevmBytes:    base64.StdEncoding.EncodeToString(currentDevm),
			ApprovedDevmSHA:     "absent",
			ApprovedMeSHA:       "absent",
			ApprovedScriptSHA:   "absent",
			ApprovedMeScriptSHA: "absent",
		}
		if currentMe != nil {
			s := base64.StdEncoding.EncodeToString(currentMe)
			resp.CurrentMeBytes = &s
		}
		if currentScript != nil {
			s := base64.StdEncoding.EncodeToString(currentScript)
			resp.CurrentScriptBytes = &s
		}
		if currentMeScript != nil {
			s := base64.StdEncoding.EncodeToString(currentMeScript)
			resp.CurrentMeScriptBytes = &s
		}
		if hasSnap {
			resp.ApprovedDevmSHA = approve.HashFile(snap.DevmYAML)
			resp.ApprovedMeSHA = approve.HashFile(snap.MeYAML)
			resp.ApprovedScriptSHA = approve.HashFile(snap.DevmSH)
			resp.ApprovedMeScriptSHA = approve.HashFile(snap.DevmMeSH)
			s := base64.StdEncoding.EncodeToString(snap.DevmYAML)
			resp.ApprovedDevmBytes = &s
			if snap.MeYAML != nil {
				s := base64.StdEncoding.EncodeToString(snap.MeYAML)
				resp.ApprovedMeBytes = &s
			}
			if snap.DevmSH != nil {
				s := base64.StdEncoding.EncodeToString(snap.DevmSH)
				resp.ApprovedScriptBytes = &s
			}
			if snap.DevmMeSH != nil {
				s := base64.StdEncoding.EncodeToString(snap.DevmMeSH)
				resp.ApprovedMeScriptBytes = &s
			}
			since := snap.Manifest.Timestamp.Format("2006-01-02T15:04:05Z")
			src := snap.Manifest.Source
			resp.ApprovedSince = &since
			resp.ApprovedSource = &src
			resp.Diverged = resp.CurrentDevmSHA != resp.ApprovedDevmSHA ||
				resp.CurrentMeSHA != resp.ApprovedMeSHA ||
				resp.CurrentScriptSHA != resp.ApprovedScriptSHA ||
				resp.CurrentMeScriptSHA != resp.ApprovedMeScriptSHA
		} else {
			resp.Diverged = true
		}
		if meta, ok, err := ReadLastProposal(cfg, project); err != nil {
			daemonlog.Errorf("approve-state: read last-proposal: %v", err)
		} else if ok {
			resp.Proposal = meta
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
	return http.HandlerFunc(fn)
}

func handleApprove(cfg identity.Config, cache *StateCache) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "approve: POST only", http.StatusMethodNotAllowed)
			return
		}
		project := r.URL.Query().Get("project")
		if project == "" {
			http.Error(w, "approve: project query param required", http.StatusBadRequest)
			return
		}
		macCwd := resolveMacCwdForRead(cache, project, r.URL.Query().Get("cwd"))
		if macCwd == "" {
			http.Error(w, fmt.Sprintf("approve: project %q not started; run `devm start` from its directory first", project), http.StatusPreconditionFailed)
			return
		}
		currentDevm, err := os.ReadFile(filepath.Join(macCwd, "devm.yaml"))
		if err != nil {
			http.Error(w, fmt.Sprintf("approve: read devm.yaml: %v", err), http.StatusInternalServerError)
			return
		}
		var currentMe []byte
		if b, err := os.ReadFile(filepath.Join(macCwd, "devm.me.yaml")); err == nil {
			currentMe = b
		} else if !errors.Is(err, os.ErrNotExist) {
			http.Error(w, fmt.Sprintf("approve: read devm.me.yaml: %v", err), http.StatusInternalServerError)
			return
		}
		var currentScript []byte
		if b, err := os.ReadFile(filepath.Join(macCwd, "devm.sh")); err == nil {
			currentScript = b
		} else if !errors.Is(err, os.ErrNotExist) {
			http.Error(w, fmt.Sprintf("approve: read devm.sh: %v", err), http.StatusInternalServerError)
			return
		}
		var currentMeScript []byte
		if b, err := os.ReadFile(filepath.Join(macCwd, "devm.me.sh")); err == nil {
			currentMeScript = b
		} else if !errors.Is(err, os.ErrNotExist) {
			http.Error(w, fmt.Sprintf("approve: read devm.me.sh: %v", err), http.StatusInternalServerError)
			return
		}
		store := approve.NewStore(cfg)
		if err := store.Write(project, currentDevm, currentMe, currentScript, currentMeScript, "user"); err != nil {
			http.Error(w, fmt.Sprintf("approve: write snapshot: %v", err), http.StatusInternalServerError)
			return
		}

		if err := ClearLastProposal(cfg, project); err != nil {
			daemonlog.Errorf("approve: clear last-proposal: %v", err)
		}

		// The just-written snapshot IS the current bytes — current and
		// approved converge by definition, so Diverged is always false
		// immediately after a successful approve. cache is non-nil here:
		// the projectConfigPath lookup above already dereferenced it.
		devmSHA := approve.HashFile(currentDevm)
		meSHA := approve.HashFile(currentMe)
		since := time.Now()
		cache.SetApproveState(project, ApproveStateSummary{
			Diverged:        false,
			CurrentDevmSHA:  devmSHA,
			ApprovedDevmSHA: devmSHA,
			CurrentMeSHA:    meSHA,
			ApprovedMeSHA:   meSHA,
			ApprovedSince:   &since,
		})

		w.WriteHeader(http.StatusNoContent)
	})
}

// bootstrapApprovedSnapshotOnFirstRun writes the project's current
// devm.yaml + devm.me.yaml (if present) as the initial approved
// snapshot IF no snapshot exists yet. First-run bootstrap: the file
// as it is at the first cold-start becomes the baseline. configDir is
// the project's Mac cwd — where devm.yaml lives.
func bootstrapApprovedSnapshotOnFirstRun(cfg identity.Config, projectID, configDir string) error {
	store := approve.NewStore(cfg)
	_, hasSnap, err := store.Read(projectID)
	if err != nil {
		return fmt.Errorf("bootstrap-approve: read: %w", err)
	}
	if hasSnap {
		return nil
	}
	currentDevm, err := os.ReadFile(filepath.Join(configDir, "devm.yaml"))
	if err != nil {
		return fmt.Errorf("bootstrap-approve: read devm.yaml: %w", err)
	}
	var currentMe []byte
	if b, err := os.ReadFile(filepath.Join(configDir, "devm.me.yaml")); err == nil {
		currentMe = b
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("bootstrap-approve: read devm.me.yaml: %w", err)
	}
	var currentScript []byte
	if b, err := os.ReadFile(filepath.Join(configDir, "devm.sh")); err == nil {
		currentScript = b
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("bootstrap-approve: read devm.sh: %w", err)
	}
	var currentMeScript []byte
	if b, err := os.ReadFile(filepath.Join(configDir, "devm.me.sh")); err == nil {
		currentMeScript = b
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("bootstrap-approve: read devm.me.sh: %w", err)
	}
	return store.Write(projectID, currentDevm, currentMe, currentScript, currentMeScript, "user")
}

const approveRefusalMessage = `devm.yaml (or devm.me.yaml, devm.sh, devm.me.sh) has changed since it was last approved.
Approve the change:
  - Click the devm menu bar icon → Review, or
  - Run ` + "`devm approve`" + ` in this terminal to review + approve inline.`

// isApproveDiverged reports whether devm.yaml/devm.me.yaml/devm.sh/
// devm.me.sh at configDir (the project's Mac cwd) differ from the
// last-approved snapshot.
func isApproveDiverged(cfg identity.Config, projectID, configDir string) (bool, error) {
	currentDevm, err := os.ReadFile(filepath.Join(configDir, "devm.yaml"))
	if err != nil {
		return false, fmt.Errorf("read devm.yaml: %w", err)
	}
	var currentMe []byte
	if b, err := os.ReadFile(filepath.Join(configDir, "devm.me.yaml")); err == nil {
		currentMe = b
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("read devm.me.yaml: %w", err)
	}
	var currentScript []byte
	if b, err := os.ReadFile(filepath.Join(configDir, "devm.sh")); err == nil {
		currentScript = b
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("read devm.sh: %w", err)
	}
	var currentMeScript []byte
	if b, err := os.ReadFile(filepath.Join(configDir, "devm.me.sh")); err == nil {
		currentMeScript = b
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("read devm.me.sh: %w", err)
	}
	store := approve.NewStore(cfg)
	snap, hasSnap, err := store.Read(projectID)
	if err != nil {
		return false, err
	}
	if !hasSnap {
		return true, nil
	}
	return approve.HashFile(currentDevm) != approve.HashFile(snap.DevmYAML) ||
		approve.HashFile(currentMe) != approve.HashFile(snap.MeYAML) ||
		approve.HashFile(currentScript) != approve.HashFile(snap.DevmSH) ||
		approve.HashFile(currentMeScript) != approve.HashFile(snap.DevmMeSH), nil
}
