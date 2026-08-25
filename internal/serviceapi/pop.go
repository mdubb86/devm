// Pop is the daemon-side entry point for guest "pop <path>" requests.
// A per-project HTTP listener (spawned at /vm/start) serves POST /pop:
//
//	Body: {"arg": "<path arg>", "cwd": "<abs guest cwd>",
//	       "open_args": ["-a", "Preview"]}
//
// The handler resolves the arg (cwd-first, then project root),
// translates the guest path to Mac storage, then hands the pretty
// .vm/-form Mac path to `open`.
//
// Softnet forwards guest TCP 192.168.127.1:81 to this listener — see
// internal/softnet/egress.go's Pop branch and internal/serviceapi/
// vm.go's /vm/start.
package serviceapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/mdubb86/devm/internal/daemonlog"
	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/repohelpers"
)

// popExecOpen is the test-injection seam for the `open` invocation.
// Production uses exec.CommandContext; tests override to record argv
// without launching anything.
var popExecOpen = func(ctx context.Context, args ...string) error {
	return exec.CommandContext(ctx, "open", args...).Run()
}

type popRequest struct {
	Arg      string   `json:"arg"`
	Cwd      string   `json:"cwd"`
	OpenArgs []string `json:"open_args,omitempty"`
}

func handlePop(w http.ResponseWriter, r *http.Request, projectName string, registry []WorkspaceEntry) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req popRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("pop: malformed body: %v", err), http.StatusBadRequest)
		return
	}
	if req.Arg == "" || req.Cwd == "" {
		http.Error(w, "pop: arg and cwd are required", http.StatusBadRequest)
		return
	}

	// Find this project's workspace entry — the listener is per-project,
	// so we know which one to use.
	var entry *WorkspaceEntry
	for i := range registry {
		if registry[i].ProjectName == projectName {
			entry = &registry[i]
			break
		}
	}
	if entry == nil {
		http.Error(w, fmt.Sprintf("pop: no workspace registry entry for project %q", projectName), http.StatusInternalServerError)
		return
	}

	// Cwd must be inside this project's guest tree — a guest process
	// sending a cwd outside its own project is either a client bug or a
	// spoofing attempt.
	if req.Cwd != entry.GuestPath && !strings.HasPrefix(req.Cwd, entry.GuestPath+"/") {
		http.Error(w, fmt.Sprintf("pop: cwd %q is outside project guest tree %q", req.Cwd, entry.GuestPath), http.StatusBadRequest)
		return
	}

	// Candidate 1: cwd + arg (unless arg is absolute).
	// Candidate 2: project root + arg.
	// For both, translate to storage, stat, and pick whichever exists first.
	var candidates []string
	if filepath.IsAbs(req.Arg) {
		candidates = []string{req.Arg}
	} else {
		candidates = []string{
			filepath.Join(req.Cwd, req.Arg),
			filepath.Join(entry.GuestPath, req.Arg),
		}
	}

	var storagePath string
	var guestFound string
	for _, guestCand := range candidates {
		macStorage, err := repohelpers.TranslateGuestPath(guestCand, toPathEntries(registry))
		if err != nil {
			continue
		}
		if _, statErr := os.Stat(macStorage); statErr == nil {
			storagePath = macStorage
			guestFound = guestCand
			break
		}
	}

	if storagePath == "" {
		http.Error(w, fmt.Sprintf("pop: no such file %q relative to %s or project root %s", req.Arg, req.Cwd, entry.GuestPath), http.StatusNotFound)
		return
	}

	// Convert the found storage path back to the pretty .vm/-symlink
	// form so `open` shows apps a readable path like
	// "sewtrue/.vm/src/foo.png" instead of the raw
	// "~/Library/Application Support/devm/volumes/..." form.
	rel, err := filepath.Rel(entry.GuestPath, guestFound)
	if err != nil {
		http.Error(w, fmt.Sprintf("pop: internal path error: %v", err), http.StatusInternalServerError)
		return
	}
	pretty := filepath.Join(entry.GuestPath, ".vm", rel)

	openArgs := append([]string{pretty}, req.OpenArgs...)
	if err := popExecOpen(r.Context(), openArgs...); err != nil {
		daemonlog.Errorf("serviceapi: pop: open %s: %v", pretty, err)
		http.Error(w, fmt.Sprintf("pop: open failed: %v", err), http.StatusInternalServerError)
		return
	}

	log.Printf("serviceapi: pop: opened %s (project %s)", pretty, projectName)
	fmt.Fprintln(w, pretty)
}

// toPathEntries narrows a []WorkspaceEntry down to the minimal shape
// repohelpers.TranslateGuestPath needs, avoiding an import cycle
// between serviceapi and repohelpers.
func toPathEntries(registry []WorkspaceEntry) []repohelpers.WorkspacePathEntry {
	entries := make([]repohelpers.WorkspacePathEntry, len(registry))
	for i, w := range registry {
		entries[i] = repohelpers.WorkspacePathEntry{GuestPath: w.GuestPath, StoragePath: w.StoragePath}
	}
	return entries
}

// popListeners tracks each running project's pop HTTP listener so
// /vm/stop can close it by project name.
var popListeners sync.Map // projectName -> net.Listener

// servePopListener runs a minimal HTTP server on ln that dispatches
// POST /pop to handlePop for the given project. The workspace registry
// is fetched fresh per-request so hot-changed state is picked up.
func servePopListener(ln net.Listener, cfg identity.Config, projectName string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/pop", func(w http.ResponseWriter, r *http.Request) {
		reg, err := listWorkspaces(cfg)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		handlePop(w, r, projectName, reg)
	})
	srv := &http.Server{Handler: mux}
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
		daemonlog.Errorf("serviceapi: pop: listener for %s exited: %v", projectName, err)
	}
}

// closePopListener closes and forgets projectName's pop listener, if
// any. Called from /vm/stop teardown.
func closePopListener(projectName string) {
	if v, ok := popListeners.LoadAndDelete(projectName); ok {
		if ln, ok := v.(net.Listener); ok {
			ln.Close()
		}
	}
}
