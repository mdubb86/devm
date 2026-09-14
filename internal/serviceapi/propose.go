// propose is the daemon-side entry point for guest "propose" requests.
// A per-project HTTP listener (spawned at /vm/start) serves POST /propose:
//
//	Body: {"yaml": "<base64>", "cwd": "<abs guest cwd>",
//	       "branch": "<git branch or empty>",
//	       "reason": "<--reason arg or empty>",
//	       "kind": "devm.yaml"}
//
// The handler validates the YAML through schema.CheckUnknownKeys + a
// strict (KnownFields) decode + schema.Config.Validate — the same
// checks internal/config.Load applies to the base devm.yaml, minus the
// directory-scoped concerns (devm.me.yaml merge, $WORKSPACE env
// resolution, root-relative volume/label checks) that don't apply to a
// proposal validated before it has ever touched disk. It then
// atomically writes the YAML to <MacCwd>/<kind> and records
// attribution to <RuntimeDir>/<projectID>/last-proposal.json. The
// Approve gate (Piece 1) fires on the next gated command as if a human
// had edited the file.
//
// Softnet forwards guest TCP 192.168.127.1:82 to this listener — see
// internal/softnet/egress.go's Propose branch and internal/serviceapi/
// vm.go's /vm/start.
package serviceapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/mdubb86/devm/internal/daemonlog"
	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/schema"
	"gopkg.in/yaml.v3"
)

type proposeRequest struct {
	YAML   string `json:"yaml"`
	Cwd    string `json:"cwd"`
	Branch string `json:"branch"`
	Reason string `json:"reason"`
	Kind   string `json:"kind"`
}

// handleProposeForProject returns the per-project POST /propose
// handler. Route registered by vm.go's /vm/start wiring.
func handleProposeForProject(cfg identity.Config, projectName string, cache *StateCache) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "propose: POST only", http.StatusMethodNotAllowed)
			return
		}
		var req proposeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf("propose: decode body: %v", err), http.StatusBadRequest)
			return
		}
		if req.Kind != "devm.yaml" {
			http.Error(w, fmt.Sprintf("propose: unsupported kind %q (only devm.yaml)", req.Kind), http.StatusBadRequest)
			return
		}
		yamlBytes, err := base64.StdEncoding.DecodeString(req.YAML)
		if err != nil {
			http.Error(w, fmt.Sprintf("propose: base64 decode: %v", err), http.StatusBadRequest)
			return
		}

		if err := schema.CheckUnknownKeys(yamlBytes); err != nil {
			http.Error(w, fmt.Sprintf("propose: yaml: %v", err), http.StatusBadRequest)
			return
		}
		var parsed schema.Config
		dec := yaml.NewDecoder(bytes.NewReader(yamlBytes))
		dec.KnownFields(true)
		if err := dec.Decode(&parsed); err != nil {
			http.Error(w, fmt.Sprintf("propose: yaml parse: %v", err), http.StatusBadRequest)
			return
		}
		if err := parsed.Validate(); err != nil {
			http.Error(w, fmt.Sprintf("propose: yaml validate: %v", err), http.StatusBadRequest)
			return
		}

		row, ok := cache.ProjectRow(projectName)
		if !ok || row.MacCwd == "" {
			daemonlog.Errorf("serviceapi: propose: no mac_cwd for project %s", projectName)
			http.Error(w, fmt.Sprintf("propose: no mac_cwd for project %q", projectName), http.StatusInternalServerError)
			return
		}

		// Atomic tmp+rename write of the YAML.
		targetPath := filepath.Join(row.MacCwd, req.Kind)
		tmp := targetPath + ".tmp"
		if err := os.WriteFile(tmp, yamlBytes, 0o644); err != nil {
			daemonlog.Errorf("serviceapi: propose: write tmp for %s: %v", projectName, err)
			http.Error(w, fmt.Sprintf("propose: write tmp: %v", err), http.StatusInternalServerError)
			return
		}
		if err := os.Rename(tmp, targetPath); err != nil {
			_ = os.Remove(tmp)
			daemonlog.Errorf("serviceapi: propose: rename for %s: %v", projectName, err)
			http.Error(w, fmt.Sprintf("propose: rename: %v", err), http.StatusInternalServerError)
			return
		}

		// Record attribution metadata.
		if err := WriteLastProposal(cfg, projectName, ProposalMetadata{
			Cwd:       req.Cwd,
			Branch:    req.Branch,
			Reason:    req.Reason,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Source:    "guest",
			Kind:      req.Kind,
		}); err != nil {
			daemonlog.Errorf("serviceapi: propose: write last-proposal metadata for %s: %v", projectName, err)
			http.Error(w, fmt.Sprintf("propose: metadata: %v", err), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	})
}
