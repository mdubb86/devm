// passthrough_request.go — the daemon endpoint gdevm hits over
// softnet to REQUEST an egress passthrough window. Records a pending
// request awaiting Mac-side approval; the window itself opens only
// when a human runs `devm passthrough approve`.
//
// Route: POST /passthrough, shared with the propose handler on the
// per-project TCP listener softnet forwards guest 192.168.127.1:82 to.
// See serveProposeListener in propose.go.
package serviceapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/mdubb86/devm/internal/daemonlog"
	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/schema"
)

// maxPassthroughReqBodyBytes caps a guest passthrough request. The
// body is metadata-only; 64 KiB is generous.
const maxPassthroughReqBodyBytes = 64 << 10

// passthroughRequest is the wire body `gdevm passthrough` posts.
type passthroughRequest struct {
	Reason          string `json:"reason"`
	DurationSeconds int    `json:"duration_seconds,omitempty"`
	Cwd             string `json:"cwd,omitempty"`
	Branch          string `json:"branch,omitempty"`
	Source          string `json:"source,omitempty"`
}

// handlePassthroughRequestForProject returns the per-project
// POST /passthrough handler serving the guest side.
func handlePassthroughRequestForProject(cfg identity.Config, cache *StateCache, projectName string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "passthrough: POST only", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPassthroughReqBodyBytes))
		if err != nil {
			http.Error(w, fmt.Sprintf("passthrough: read body: %v", err), http.StatusBadRequest)
			return
		}
		var req passthroughRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, fmt.Sprintf("passthrough: bad json: %v", err), http.StatusBadRequest)
			return
		}
		if req.Reason == "" {
			http.Error(w, "passthrough: reason required — pass --reason \"why the window is needed\"", http.StatusBadRequest)
			return
		}

		row, _ := cache.ProjectRow(projectName)
		macCwd := row.MacCwd
		if macCwd == "" {
			http.Error(w,
				fmt.Sprintf("passthrough: project %q not started; run `devm start` from its directory first", projectName),
				http.StatusPreconditionFailed)
			return
		}

		// Guest gate: refuse if devm.yaml disables gdevm passthrough.
		if yamlBytes, ferr := os.ReadFile(filepath.Join(macCwd, "devm.yaml")); ferr == nil {
			var current schema.Config
			if verr := yamlDecodeStrict(yamlBytes, &current); verr == nil {
				if !current.GuestPassthroughAllowed() {
					http.Error(w,
						"passthrough: guest.passthrough is disabled in devm.yaml — the Mac side is not accepting passthrough requests from the guest",
						http.StatusForbidden)
					return
				}
			}
		}

		source := req.Source
		if source == "" {
			source = "guest"
		}

		pending := PendingPassthroughRequest{
			Reason:          req.Reason,
			DurationSeconds: req.DurationSeconds,
			Cwd:             req.Cwd,
			Branch:          req.Branch,
			Timestamp:       time.Now().UTC().Format(time.RFC3339),
			Source:          source,
		}
		if err := WritePendingPassthrough(cfg, projectName, pending); err != nil {
			daemonlog.Errorf("serviceapi: pending-passthrough for %s: %v", projectName, err)
			http.Error(w, fmt.Sprintf("passthrough: record request: %v", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprintln(w, "passthrough: request submitted — awaiting Mac-side approval via `devm passthrough approve`")
	})
}
