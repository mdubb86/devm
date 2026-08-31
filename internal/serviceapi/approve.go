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

	"github.com/mdubb86/devm/internal/approve"
	"github.com/mdubb86/devm/internal/identity"
)

type approveStateResponse struct {
	Project             string  `json:"project"`
	Diverged            bool    `json:"diverged"`
	CurrentDevmSHA      string  `json:"current_devm_sha"`
	ApprovedDevmSHA     string  `json:"approved_devm_sha"`
	CurrentMeSHA        string  `json:"current_me_sha"`
	ApprovedMeSHA       string  `json:"approved_me_sha"`
	CurrentDevmBytes    string  `json:"current_devm_bytes"`
	ApprovedDevmBytes   *string `json:"approved_devm_bytes"`
	CurrentMeBytes      *string `json:"current_me_bytes"`
	ApprovedMeBytes     *string `json:"approved_me_bytes"`
	ApprovedSince       *string `json:"approved_since"`
	ApprovedSource      *string `json:"approved_source"`
	Proposal            any     `json:"proposal"` // Plan C fills this in; always nil for now.
}

func handleApproveState(cfg identity.Config) http.Handler {
	fn := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "approve-state: GET only", http.StatusMethodNotAllowed)
			return
		}
		project := r.URL.Query().Get("project")
		macCwd := r.URL.Query().Get("mac_cwd")
		if project == "" || macCwd == "" {
			http.Error(w, "approve-state: project and mac_cwd query params required", http.StatusBadRequest)
			return
		}
		currentDevm, err := os.ReadFile(filepath.Join(macCwd, "devm.yaml"))
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
		store := approve.NewStore(cfg)
		snap, hasSnap, err := store.Read(project)
		if err != nil {
			http.Error(w, fmt.Sprintf("approve-state: read snapshot: %v", err), http.StatusInternalServerError)
			return
		}
		curDevmSHA := approve.HashFile(currentDevm)
		curMeSHA := approve.HashFile(currentMe)
		resp := approveStateResponse{
			Project:          project,
			CurrentDevmSHA:   curDevmSHA,
			CurrentMeSHA:     curMeSHA,
			CurrentDevmBytes: base64.StdEncoding.EncodeToString(currentDevm),
			ApprovedDevmSHA:  "absent",
			ApprovedMeSHA:    "absent",
		}
		if currentMe != nil {
			s := base64.StdEncoding.EncodeToString(currentMe)
			resp.CurrentMeBytes = &s
		}
		if hasSnap {
			resp.ApprovedDevmSHA = approve.HashFile(snap.DevmYAML)
			resp.ApprovedMeSHA = approve.HashFile(snap.MeYAML)
			s := base64.StdEncoding.EncodeToString(snap.DevmYAML)
			resp.ApprovedDevmBytes = &s
			if snap.MeYAML != nil {
				s := base64.StdEncoding.EncodeToString(snap.MeYAML)
				resp.ApprovedMeBytes = &s
			}
			since := snap.Manifest.Timestamp.Format("2006-01-02T15:04:05Z")
			src := snap.Manifest.Source
			resp.ApprovedSince = &since
			resp.ApprovedSource = &src
			resp.Diverged = resp.CurrentDevmSHA != resp.ApprovedDevmSHA || resp.CurrentMeSHA != resp.ApprovedMeSHA
		} else {
			resp.Diverged = true
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
	return http.HandlerFunc(fn)
}

func handleApprove(cfg identity.Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "approve: POST only", http.StatusMethodNotAllowed)
			return
		}
		project := r.URL.Query().Get("project")
		macCwd := r.URL.Query().Get("mac_cwd")
		if project == "" || macCwd == "" {
			http.Error(w, "approve: project and mac_cwd query params required", http.StatusBadRequest)
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
		store := approve.NewStore(cfg)
		if err := store.Write(project, currentDevm, currentMe, "user"); err != nil {
			http.Error(w, fmt.Sprintf("approve: write snapshot: %v", err), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
