// Pop is the daemon-side entry point for guest "gdevm pop <path>"
// requests. Mounted on the per-project guest-API listener (see
// serveGuestAPIListener in propose.go) as POST /pop:
//
//	Body: {"project": "<name>", "guest_path": "<abs guest path>",
//	       "native": false, "open_args": ["-a", "Preview"]}
//
// Default (native=false): the handler builds the project's filestash
// URL for guest_path and shells `open <url>` — the Mac's default
// browser lands on filestash's view of that file.
//
// Native (native=true): the handler reverse-mirrors guest_path to its
// Mac mirror path (if the file lives in a mutagen-synced repo) and
// opens that live file; otherwise it `tart exec cat`'s the guest
// bytes into PopScratchRoot and opens the scratch copy. Same shape as
// the pre-removal cmd/devm/pop.go --native path, now server-side so
// in-guest `gdevm pop --native` works too.
//
// Softnet forwards guest TCP 192.168.127.1:81 to the guest-API
// listener — see internal/softnet/egress.go's GuestAPI branch and
// internal/serviceapi/vm.go's /vm/start.
package serviceapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"

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

// popTartExecCat is the test-injection seam for `tart exec <vm> cat
// <guestPath>` into a Mac-side scratch file. Production copies via
// tart; tests override to drop bytes directly.
var popTartExecCat = func(ctx context.Context, vmName, guestPath, macDest string) error {
	f, err := os.Create(macDest)
	if err != nil {
		return fmt.Errorf("create %s: %w", macDest, err)
	}
	defer f.Close()
	cmd := exec.CommandContext(ctx, "tart", "exec", vmName, "cat", guestPath)
	cmd.Stdout = f
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		os.Remove(macDest)
		return fmt.Errorf("tart exec cat %s: %w", guestPath, err)
	}
	return nil
}

// PopRequest is the on-wire body of POST /pop. Guest-side gdevm pop
// marshals it; the handler decodes and dispatches. The project is not
// in the body — each pop listener is per-project (bound at /vm/start)
// and already knows which project it serves.
type PopRequest struct {
	GuestPath string   `json:"guest_path"`
	Native    bool     `json:"native,omitempty"`
	IsDir     bool     `json:"is_dir,omitempty"`
	IsHTML    bool     `json:"is_html,omitempty"`
	OpenArgs  []string `json:"open_args,omitempty"`
}

// handlePop decodes a PopRequest and runs the open. Only POST is
// accepted; GET / anything else fails loud so a casual browser hit
// doesn't launch something.
func handlePop(w http.ResponseWriter, r *http.Request, cfg identity.Config, projectName string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req PopRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("pop: malformed body: %v", err), http.StatusBadRequest)
		return
	}
	if req.GuestPath == "" {
		http.Error(w, "pop: guest_path is required", http.StatusBadRequest)
		return
	}
	if !filepath.IsAbs(req.GuestPath) {
		http.Error(w, fmt.Sprintf("pop: guest_path %q must be absolute", req.GuestPath), http.StatusBadRequest)
		return
	}

	target, err := resolvePopTarget(r.Context(), cfg, projectName, req.GuestPath, req.Native, req.IsDir, req.IsHTML)
	if err != nil {
		if errors.Is(err, errPopNoSuchMirror) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		daemonlog.Errorf("serviceapi: pop: resolve %s: %v", req.GuestPath, err)
		http.Error(w, fmt.Sprintf("pop: resolve: %v", err), http.StatusInternalServerError)
		return
	}

	openArgs := append([]string{target}, req.OpenArgs...)
	if err := popExecOpen(r.Context(), openArgs...); err != nil {
		daemonlog.Errorf("serviceapi: pop: open %s: %v", target, err)
		http.Error(w, fmt.Sprintf("pop: open failed: %v", err), http.StatusInternalServerError)
		return
	}
	log.Printf("serviceapi: pop: opened %s (project %s, native=%v)", target, projectName, req.Native)
	fmt.Fprintln(w, target)
}

// errPopNoSuchMirror is returned when native=true and the guest path
// is not covered by any mirror AND tart-exec-cat failed — the handler
// surfaces it as 404 so the caller sees "no such file" rather than a
// generic 500.
var errPopNoSuchMirror = errors.New("pop: guest path not in any mirror and tart-exec-cat failed")

// resolvePopTarget returns the Mac-side argument to pass to `open`:
//
//   - native=false: the dispatched URL from popTargetURL — the preview
//     server for HTML, filestash's listing for a directory, filestash's
//     single-file view otherwise. No mirror lookup is needed.
//   - native=true + guest path in-mirror: the live Mac mirror path.
//     Edits sync back to the guest via the usual mutagen loop.
//   - native=true + out-of-mirror: a scratch copy under PopScratchRoot
//     populated via `tart exec cat`. One file per pop.
func resolvePopTarget(ctx context.Context, cfg identity.Config, projectName, guestPath string, native, isDir, isHTML bool) (string, error) {
	if !native {
		return popTargetURL(cfg, projectName, guestPath, isDir, isHTML), nil
	}

	reg, err := listWorkspaces(cfg)
	if err != nil {
		return "", fmt.Errorf("list workspaces: %w", err)
	}
	mirrorPath, mirrorErr := repohelpers.TranslateGuestPath(guestPath, toPopPathEntries(reg))
	if mirrorErr == nil {
		if _, statErr := os.Stat(mirrorPath); statErr == nil {
			return mirrorPath, nil
		}
	}

	// Out-of-mirror (or in-mirror but the Mac side hasn't materialised
	// the file yet): cp guest bytes to a scratch path, open that. The
	// scratch name is stable per (project, guestPath) so repeated pops
	// reuse the same file.
	scratchRoot := PopScratchRoot(cfg)
	if err := os.MkdirAll(scratchRoot, 0o755); err != nil {
		return "", fmt.Errorf("mkdir scratch root: %w", err)
	}
	dest := filepath.Join(scratchRoot, popScratchName(projectName, guestPath))
	vmName := projectName // tart vm name == project name
	if err := popTartExecCat(ctx, vmName, guestPath, dest); err != nil {
		return "", errPopNoSuchMirror
	}
	return dest, nil
}

// popTargetURL builds the Mac-side URL a pop opens. HTML files go to the
// preview server so relative and absolute asset paths resolve. Directories
// go to filestash's listing route; other files to its single-file view
// (filestash's /files route treats a file path as a listing and fails).
// filestash omits the backend label from URLs because the direct-strategy
// preset configures a single `local` connection. IsHTML wins over IsDir.
func popTargetURL(cfg identity.Config, projectName, guestPath string, isDir, isHTML bool) string {
	u := url.URL{Scheme: "https"}
	switch {
	case isHTML:
		u.Host = "preview." + projectName + "." + cfg.TLD
		u.Path = guestPath
	case isDir:
		u.Host = "files." + projectName + "." + cfg.TLD
		u.Path = "/files" + guestPath + "/"
	default:
		u.Host = "files." + projectName + "." + cfg.TLD
		u.Path = "/view" + guestPath
	}
	return u.String()
}

// popScratchName hashes (project, guestPath) to a stable short name so
// repeated pops of the same path reuse the same scratch file, and so
// pops across projects don't collide on identical basenames.
func popScratchName(project, guestPath string) string {
	sum := sha256.Sum256([]byte(project + "\x00" + guestPath))
	base := hex.EncodeToString(sum[:])[:16]
	ext := filepath.Ext(guestPath)
	return base + ext
}

// toPopPathEntries narrows a []WorkspaceEntry down to the minimal
// shape repohelpers.TranslateGuestPath needs. Avoids an import cycle
// between serviceapi and repohelpers while giving the pop handler a
// stable call shape.
func toPopPathEntries(reg []WorkspaceEntry) []repohelpers.WorkspacePathEntry {
	out := make([]repohelpers.WorkspacePathEntry, len(reg))
	for i, w := range reg {
		out[i] = repohelpers.WorkspacePathEntry{GuestPath: w.GuestPath, StoragePath: w.StoragePath}
	}
	return out
}
