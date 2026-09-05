package serviceapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/mdubb86/devm/internal/daemonlog"
	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/mutagen"
)

type popSessionRequest struct {
	Project   string `json:"project"`
	GuestPath string `json:"guest_path"`
	IsDir     bool   `json:"is_dir"`
}

type popSessionResponse struct {
	MacPath string `json:"mac_path"`
}

// popSessionHandler is the UDS POST /pop-session handler. The Mac CLI
// hits this for a path that isn't inside any mirrored repo/volume — the
// guest path is already absolute and is passed verbatim to the shared
// store; the handler does not stat it (the Mac CLI has no guest
// filesystem access to stat against).
func popSessionHandler(
	cfg identity.Config,
	store *PopSessionStore,
	cli *mutagen.CLI,
	guestSSHTargetFor func(project string) string,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req popSessionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf("pop-session: malformed body: %v", err), http.StatusBadRequest)
			return
		}
		if req.Project == "" || req.GuestPath == "" {
			http.Error(w, "pop-session: project and guest_path are required", http.StatusBadRequest)
			return
		}
		if !filepath.IsAbs(req.GuestPath) {
			http.Error(w, fmt.Sprintf("pop-session: guest_path %q must be absolute", req.GuestPath), http.StatusBadRequest)
			return
		}
		kind := PopKindFile
		if req.IsDir {
			kind = PopKindDir
		}
		guestSSHTarget := guestSSHTargetFor(req.Project)
		if guestSSHTarget == "" {
			http.Error(w, fmt.Sprintf("pop-session: project %q not running", req.Project), http.StatusNotFound)
			return
		}
		session, _, err := store.GetOrCreate(cfg, req.Project, req.GuestPath, kind, func(ps *PopSession) error {
			return CreatePopSyncSession(cli, cfg, guestSSHTarget, ps)
		})
		if err != nil {
			daemonlog.Errorf("serviceapi: pop-session: create for %s: %v", req.GuestPath, err)
			http.Error(w, fmt.Sprintf("pop-session: create: %v", err), http.StatusInternalServerError)
			return
		}
		target := session.MacDir
		if session.Kind == PopKindFile {
			target = filepath.Join(session.MacDir, session.TargetName)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(popSessionResponse{MacPath: target})
	}
}

// popSessionSummaryHandler is the UDS GET /pop-session-summary handler.
// Backs `devm status` — informational only, so it reports a count and
// oldest-session age rather than the sessions themselves.
func popSessionSummaryHandler(store *PopSessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "pop-session-summary: GET only", http.StatusMethodNotAllowed)
			return
		}
		project := r.URL.Query().Get("project")
		if project == "" {
			http.Error(w, "pop-session-summary: project required", http.StatusBadRequest)
			return
		}
		sessions := store.ListForProject(project)
		var oldest int64
		if len(sessions) > 0 {
			oldest = int64(time.Since(sessions[0].CreatedAt).Round(time.Second).Seconds())
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"count":              len(sessions),
			"oldest_age_seconds": oldest,
		})
	}
}

// RegisterPopSessionHandler installs the /pop-session and
// /pop-session-summary endpoints on the daemon UDS.
func RegisterPopSessionHandler(
	server *Server,
	cfg identity.Config,
	store *PopSessionStore,
	cli *mutagen.CLI,
	guestSSHTargetFor func(project string) string,
) {
	server.Register("/pop-session", popSessionHandler(cfg, store, cli, guestSSHTargetFor))
	server.Register("/pop-session-summary", popSessionSummaryHandler(store))
}
