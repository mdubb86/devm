// snapshot_handler wires the five /snapshot/* endpoints the CLI and
// guest-side gdevm use to drive the per-entry capture / restore /
// list / prune / show flows that internal/snapshot defines. Handlers
// are thin: parse body or query, validate the entry name against the
// project's declared snapshots, hand off to internal/snapshot, write
// the result as JSON.
//
// The same five handlers mount on both the Mac-side unix-socket mux
// (project named in the body or query) and the per-project guest-API
// mux (project pinned at registration time via perProjectName).
package serviceapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/mdubb86/devm/internal/config"
	"github.com/mdubb86/devm/internal/daemonlog"
	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/snapshot"
)

// maxSnapshotBodyBytes caps a snapshot request body. All bodies are
// small JSON blobs; 64 KiB is well above any legitimate payload and
// stops a runaway caller from streaming unbounded JSON.
const maxSnapshotBodyBytes = 64 << 10

// captureFn is the test seam for snapshot.Capture. Tests substitute
// it to observe the CaptureConfig the handler constructs without
// actually running a backup.
var captureFn = snapshot.Capture

// restoreFn is the test seam for snapshot.Restore. Tests substitute
// it to observe the RestoreConfig the handler constructs.
var restoreFn = snapshot.Restore

// createRequest is the JSON body of POST /snapshot/create.
type createRequest struct {
	Project string `json:"project"`
	Entry   string `json:"entry"`
	Source  string `json:"source"`
}

// restoreRequest is the JSON body of POST /snapshot/restore.
type restoreRequest struct {
	Project string `json:"project"`
	Entry   string `json:"entry"`
	TS      string `json:"ts"`
	// Force bypasses the mode/path drift refusal. Wired from the CLI's
	// `--force` flag.
	Force bool `json:"force"`
}

// restoreResponse is the JSON body of POST /snapshot/restore's reply.
// Restored distinguishes "archive found and replayed" from "nothing to
// restore yet" — install hooks on first boot see the latter without a
// failure.
type restoreResponse struct {
	Restored bool `json:"restored"`
}

// clearRequest is the JSON body of POST /snapshot/clear.
type clearRequest struct {
	Project string `json:"project"`
	Entry   string `json:"entry"`
}

// clearResponse is the reply body of POST /snapshot/clear.
type clearResponse struct {
	Removed int `json:"removed"`
}

// pruneRequest is the JSON body of POST /snapshot/prune.
type pruneRequest struct {
	Project string `json:"project"`
	Entry   string `json:"entry"`
	Keep    int    `json:"keep"`
}

// pruneResponse is the JSON body of POST /snapshot/prune's reply.
type pruneResponse struct {
	Removed []string `json:"removed"`
}

// validSources is the whitelisted set accepted on /snapshot/create.
// Anything else is a programming error on the caller side; refuse
// loudly rather than silently coercing.
var validSources = map[snapshot.Source]bool{
	snapshot.SourceCLI:        true,
	snapshot.SourceOnStop:     true,
	snapshot.SourceOnTeardown: true,
	snapshot.SourceCron:       true,
}

// writeSnapshotError sends an {"error": "..."} JSON body with the
// given status code.
func writeSnapshotError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// writeSnapshotJSON sends 200 with body as JSON.
func writeSnapshotJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
}

// decodeJSONBody reads w's body (capped) into dst. On decode failure
// writes a 400 error body and returns false — callers stop on false.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxSnapshotBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeSnapshotError(w, http.StatusBadRequest, fmt.Sprintf("decode body: %v", err))
		return false
	}
	return true
}

// resolveProject pins the handler's project. If perProjectName is set
// (guest-API shape), that wins. Otherwise fromRequest supplies it (the
// Mac daemon-socket shape). An empty name is a 400.
func resolveProject(perProjectName, fromRequest string, w http.ResponseWriter) (string, bool) {
	if perProjectName != "" {
		return perProjectName, true
	}
	if fromRequest == "" {
		writeSnapshotError(w, http.StatusBadRequest, "project is required")
		return "", false
	}
	return fromRequest, true
}

// loadProjectConfig resolves the project's MacCwd from the state
// cache and returns the fully validated schema.Config via config.Load
// — same path handleProposeForProject takes when it needs the current
// on-disk config. Returns (nil, 404) if the cache has no row for the
// project (never started), or (nil, 400) if devm.yaml is malformed or
// fails validation.
func loadProjectConfig(cache *StateCache, name string, w http.ResponseWriter) (*schema.Config, bool) {
	row, ok := cache.ProjectRow(name)
	if !ok || row.MacCwd == "" {
		writeSnapshotError(w, http.StatusNotFound, fmt.Sprintf("project %q not found", name))
		return nil, false
	}
	cfg, err := config.Load(row.MacCwd)
	if err != nil {
		writeSnapshotError(w, http.StatusBadRequest, fmt.Sprintf("load devm.yaml: %v", err))
		return nil, false
	}
	return &cfg, true
}

// requireEntry rejects a 400 when the entry name isn't declared in
// cfg.Snapshots.
func requireEntry(cfg *schema.Config, entry string, w http.ResponseWriter) bool {
	if entry == "" {
		writeSnapshotError(w, http.StatusBadRequest, "entry is required")
		return false
	}
	if _, ok := cfg.Snapshots.Get(entry); !ok {
		writeSnapshotError(w, http.StatusBadRequest,
			fmt.Sprintf("entry %q is not declared in devm.yaml snapshots:", entry))
		return false
	}
	return true
}

// resolveKeep returns the effective keep count for a pruning
// post-capture: the request's explicit value if set, otherwise the
// entry's declared `keep` field, or zero if neither is set (meaning
// "do not prune").
func resolveKeep(cfg *schema.Config, entry string) int {
	snap, ok := cfg.Snapshots.Get(entry)
	if !ok || snap.Keep == nil {
		return 0
	}
	return *snap.Keep
}

// resolvePhaseTimeouts returns the (pre, step, post) deadlines for one
// snapshot entry, falling back to the schema's documented defaults for
// any phase the yaml did not set. Validate has already confirmed any
// declared value parses as a Go duration.
func resolvePhaseTimeouts(cfg *schema.Config, entry string) (pre, step, post time.Duration) {
	pre = schema.DefaultPreTimeout
	step = schema.DefaultStepTimeout
	post = schema.DefaultPostTimeout
	snap, ok := cfg.Snapshots.Get(entry)
	if !ok || snap.Timeout == nil {
		return
	}
	if d, ok := snap.Timeout.PreDuration(); ok {
		pre = d
	}
	if d, ok := snap.Timeout.StepDuration(); ok {
		step = d
	}
	if d, ok := snap.Timeout.PostDuration(); ok {
		post = d
	}
	return
}

// registerSnapshotRoutes mounts the six snapshot endpoints on mux.
// perProjectName, when non-empty, pins the project (guest-API mux);
// when empty, the handlers read "project" from the request body/query
// (Mac-side daemon socket mux).
func registerSnapshotRoutes(mux *http.ServeMux, cfg identity.Config, cache *StateCache, tr *tart.Tart, perProjectName string) {
	mux.HandleFunc("/snapshot/create", func(w http.ResponseWriter, r *http.Request) {
		handleSnapshotCreate(w, r, cfg, cache, tr, perProjectName)
	})
	mux.HandleFunc("/snapshot/restore", func(w http.ResponseWriter, r *http.Request) {
		handleSnapshotRestore(w, r, cfg, cache, tr, perProjectName)
	})
	mux.HandleFunc("/snapshot/list", func(w http.ResponseWriter, r *http.Request) {
		handleSnapshotList(w, r, cfg, cache, perProjectName)
	})
	mux.HandleFunc("/snapshot/prune", func(w http.ResponseWriter, r *http.Request) {
		handleSnapshotPrune(w, r, cfg, cache, perProjectName)
	})
	mux.HandleFunc("/snapshot/show", func(w http.ResponseWriter, r *http.Request) {
		handleSnapshotShow(w, r, cfg, cache, perProjectName)
	})
	mux.HandleFunc("/snapshot/clear", func(w http.ResponseWriter, r *http.Request) {
		handleSnapshotClear(w, r, cfg, cache, perProjectName)
	})
}

// RegisterSnapshotHandlers wires the six /snapshot/* endpoints onto
// the daemon's main unix-socket mux (Mac CLI side). The guest-API
// listener registers its own copy inside serveGuestAPIListener with
// perProjectName pinned; here perProjectName is "" so handlers read
// the project name from the request body or query.
func RegisterSnapshotHandlers(s *Server, cfg identity.Config, cache *StateCache, tr *tart.Tart) {
	s.Register("/snapshot/create", func(w http.ResponseWriter, r *http.Request) {
		handleSnapshotCreate(w, r, cfg, cache, tr, "")
	})
	s.Register("/snapshot/restore", func(w http.ResponseWriter, r *http.Request) {
		handleSnapshotRestore(w, r, cfg, cache, tr, "")
	})
	s.Register("/snapshot/list", func(w http.ResponseWriter, r *http.Request) {
		handleSnapshotList(w, r, cfg, cache, "")
	})
	s.Register("/snapshot/prune", func(w http.ResponseWriter, r *http.Request) {
		handleSnapshotPrune(w, r, cfg, cache, "")
	})
	s.Register("/snapshot/show", func(w http.ResponseWriter, r *http.Request) {
		handleSnapshotShow(w, r, cfg, cache, "")
	})
	s.Register("/snapshot/clear", func(w http.ResponseWriter, r *http.Request) {
		handleSnapshotClear(w, r, cfg, cache, "")
	})
}

func handleSnapshotCreate(w http.ResponseWriter, r *http.Request, cfg identity.Config, cache *StateCache, tr *tart.Tart, perProjectName string) {
	if r.Method != http.MethodPost {
		writeSnapshotError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req createRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	project, ok := resolveProject(perProjectName, req.Project, w)
	if !ok {
		return
	}
	src := snapshot.Source(req.Source)
	if !validSources[src] {
		writeSnapshotError(w, http.StatusBadRequest, fmt.Sprintf("invalid source %q", req.Source))
		return
	}
	scfg, ok := loadProjectConfig(cache, project, w)
	if !ok {
		return
	}
	if !requireEntry(scfg, req.Entry, w) {
		return
	}

	snap, _ := scfg.Snapshots.Get(req.Entry)
	pre, step, post := resolvePhaseTimeouts(scfg, req.Entry)
	capCfg := snapshot.CaptureConfig{
		Ident:       cfg,
		ProjectID:   project,
		EntryName:   req.Entry,
		Source:      src,
		HookRunner:  newHookRunnerFn(tr, project),
		Keep:        resolveKeep(scfg, req.Entry),
		PreTimeout:  pre,
		StepTimeout: step,
		PostTimeout: post,
	}
	if snap.Path != nil {
		capCfg.Path = snap.Path
		capCfg.TarRunner = newTarRunnerFn(tr, project)
	}
	meta, err := captureFn(r.Context(), capCfg)
	if err != nil {
		daemonlog.Errorf("snapshot: create project=%s entry=%s: %v", project, req.Entry, err)
		writeSnapshotError(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Printf("snapshot: create: project=%s entry=%s ts=%s size=%d", project, req.Entry, meta.TS, meta.SizeBytes)
	writeSnapshotJSON(w, meta)
}

func handleSnapshotRestore(w http.ResponseWriter, r *http.Request, cfg identity.Config, cache *StateCache, tr *tart.Tart, perProjectName string) {
	if r.Method != http.MethodPost {
		writeSnapshotError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req restoreRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	project, ok := resolveProject(perProjectName, req.Project, w)
	if !ok {
		return
	}
	scfg, ok := loadProjectConfig(cache, project, w)
	if !ok {
		return
	}
	if !requireEntry(scfg, req.Entry, w) {
		return
	}

	snap, _ := scfg.Snapshots.Get(req.Entry)
	pre, step, post := resolvePhaseTimeouts(scfg, req.Entry)
	rcfg := snapshot.RestoreConfig{
		Ident:       cfg,
		ProjectID:   project,
		EntryName:   req.Entry,
		TS:          req.TS,
		HookRunner:  newHookRunnerFn(tr, project),
		Entries:     scfg.Snapshots,
		Force:       req.Force,
		PreTimeout:  pre,
		StepTimeout: step,
		PostTimeout: post,
	}
	if snap.Path != nil {
		rcfg.Path = snap.Path
		rcfg.TarRunner = newTarRunnerFn(tr, project)
	}
	restored, err := restoreFn(r.Context(), rcfg)
	if err != nil {
		daemonlog.Errorf("snapshot: restore project=%s entry=%s ts=%q: %v", project, req.Entry, req.TS, err)
		writeSnapshotError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if restored {
		log.Printf("snapshot: restore: project=%s entry=%s ts=%q", project, req.Entry, req.TS)
	} else {
		log.Printf("snapshot: restore: project=%s entry=%s nothing to restore", project, req.Entry)
	}
	writeSnapshotJSON(w, restoreResponse{Restored: restored})
}

func handleSnapshotList(w http.ResponseWriter, r *http.Request, cfg identity.Config, cache *StateCache, perProjectName string) {
	if r.Method != http.MethodGet {
		writeSnapshotError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	project, ok := resolveProject(perProjectName, r.URL.Query().Get("project"), w)
	if !ok {
		return
	}
	scfg, ok := loadProjectConfig(cache, project, w)
	if !ok {
		return
	}
	entry := r.URL.Query().Get("entry")
	var metas []snapshot.Meta
	if entry != "" {
		if !requireEntry(scfg, entry, w) {
			return
		}
		got, err := listEntry(cfg, project, entry)
		if err != nil {
			daemonlog.Errorf("snapshot: list project=%s entry=%s: %v", project, entry, err)
			writeSnapshotError(w, http.StatusInternalServerError, err.Error())
			return
		}
		metas = got
	} else {
		declared := make(map[string]bool, len(scfg.Snapshots.Order))
		for _, name := range scfg.Snapshots.Order {
			declared[name] = true
			m, err := latestMeta(cfg, project, name)
			if err != nil {
				daemonlog.Errorf("snapshot: list latest project=%s entry=%s: %v", project, name, err)
				writeSnapshotError(w, http.StatusInternalServerError, err.Error())
				return
			}
			if m == nil {
				continue
			}
			metas = append(metas, *m)
		}
		orphans, err := DiscoverOrphans(cfg, project, declared)
		if err != nil {
			daemonlog.Errorf("snapshot: list orphans project=%s: %v", project, err)
			writeSnapshotError(w, http.StatusInternalServerError, err.Error())
			return
		}
		metas = append(metas, orphans...)
	}
	// Newest first.
	sort.Slice(metas, func(i, j int) bool { return metas[i].TS > metas[j].TS })
	if metas == nil {
		metas = []snapshot.Meta{}
	}
	writeSnapshotJSON(w, metas)
}

func handleSnapshotPrune(w http.ResponseWriter, r *http.Request, cfg identity.Config, cache *StateCache, perProjectName string) {
	if r.Method != http.MethodPost {
		writeSnapshotError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req pruneRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	project, ok := resolveProject(perProjectName, req.Project, w)
	if !ok {
		return
	}
	if req.Keep <= 0 {
		writeSnapshotError(w, http.StatusBadRequest, fmt.Sprintf("keep must be positive, got %d", req.Keep))
		return
	}
	scfg, ok := loadProjectConfig(cache, project, w)
	if !ok {
		return
	}
	if !requireEntry(scfg, req.Entry, w) {
		return
	}
	dir := snapshot.EntryDir(cfg, project, req.Entry)
	removed, err := snapshot.Prune(dir, req.Keep)
	if err != nil {
		daemonlog.Errorf("snapshot: prune project=%s entry=%s keep=%d: %v", project, req.Entry, req.Keep, err)
		writeSnapshotError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if removed == nil {
		removed = []string{}
	}
	log.Printf("snapshot: prune: project=%s entry=%s keep=%d removed=%d", project, req.Entry, req.Keep, len(removed))
	writeSnapshotJSON(w, pruneResponse{Removed: removed})
}

func handleSnapshotShow(w http.ResponseWriter, r *http.Request, cfg identity.Config, cache *StateCache, perProjectName string) {
	if r.Method != http.MethodGet {
		writeSnapshotError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	project, ok := resolveProject(perProjectName, r.URL.Query().Get("project"), w)
	if !ok {
		return
	}
	scfg, ok := loadProjectConfig(cache, project, w)
	if !ok {
		return
	}
	entry := r.URL.Query().Get("entry")
	if !requireEntry(scfg, entry, w) {
		return
	}
	dir := snapshot.EntryDir(cfg, project, entry)
	ts := r.URL.Query().Get("ts")
	if ts == "" {
		latest, err := snapshot.LatestTS(dir)
		if err != nil {
			daemonlog.Errorf("snapshot: show latest project=%s entry=%s: %v", project, entry, err)
			writeSnapshotError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if latest == "" {
			writeSnapshotError(w, http.StatusNotFound, fmt.Sprintf("no archive for entry %q", entry))
			return
		}
		ts = latest
	}
	meta, err := snapshot.ReadMeta(dir, ts)
	if err != nil {
		daemonlog.Errorf("snapshot: show project=%s entry=%s ts=%s: %v", project, entry, ts, err)
		writeSnapshotError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// ReadMeta returns {Source: unknown} on a missing sidecar, even
	// when the archive is also absent — distinguish here.
	if meta.TS == "" {
		meta.TS = ts
	}
	writeSnapshotJSON(w, meta)
}

// handleSnapshotClear removes every archive and sidecar under the
// entry's directory. Unlike the other snapshot endpoints, it does NOT
// gate on `requireEntry`: the operator explicitly wants to clean up
// archives for an entry that may have been removed from devm.yaml
// (orphaned by a rename or deletion). Refusing on the yaml declaration
// would leave orphans unreachable without hand-editing the filesystem.
func handleSnapshotClear(w http.ResponseWriter, r *http.Request, cfg identity.Config, cache *StateCache, perProjectName string) {
	if r.Method != http.MethodPost {
		writeSnapshotError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req clearRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	project, ok := resolveProject(perProjectName, req.Project, w)
	if !ok {
		return
	}
	if req.Entry == "" {
		writeSnapshotError(w, http.StatusBadRequest, "entry is required")
		return
	}
	// loadProjectConfig still runs so the project itself is known; a
	// never-started project is a 404 (consistent with the other
	// endpoints). The entry name, however, is accepted as-is.
	if _, ok := loadProjectConfig(cache, project, w); !ok {
		return
	}
	removed, err := snapshot.Clear(cfg, project, req.Entry)
	if err != nil {
		daemonlog.Errorf("snapshot: clear project=%s entry=%s: %v", project, req.Entry, err)
		writeSnapshotError(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Printf("snapshot: clear: project=%s entry=%s removed=%d", project, req.Entry, removed)
	writeSnapshotJSON(w, clearResponse{Removed: removed})
}

// listEntry returns every archive's Meta under one entry's directory,
// each decoded through ReadMeta. Archive-without-sidecar entries get
// Source=unknown but still appear so operators see the bytes they can
// restore.
func listEntry(cfg identity.Config, projectID, entry string) ([]snapshot.Meta, error) {
	dir := snapshot.EntryDir(cfg, projectID, entry)
	stamps, err := snapshot.ListTimestamps(dir)
	if err != nil {
		return nil, err
	}
	out := make([]snapshot.Meta, 0, len(stamps))
	for _, ts := range stamps {
		m, err := snapshot.ReadMeta(dir, ts)
		if err != nil {
			return nil, err
		}
		if m.TS == "" {
			m.TS = ts
		}
		out = append(out, m)
	}
	return out, nil
}

// DiscoverOrphans scans the project's snapshots directory for entry
// dirs whose names are not in `declared` (yaml's current snapshots:
// order). For each orphan, it returns a Meta with Orphan=true and
// ArchiveName set to the entry name; other fields are filled from the
// latest sidecar on disk when one exists, so operators see what bytes
// are still restorable. A missing root dir is not an error — a project
// that has never captured anything has nothing to orphan.
//
// Exported so callers outside this package (orchestrator.RunStatus,
// the reconcile handler) can surface the same orphan set `/snapshot/
// list` returns without duplicating the scan.
func DiscoverOrphans(cfg identity.Config, projectID string, declared map[string]bool) ([]snapshot.Meta, error) {
	root := filepath.Join(cfg.RuntimeDir(), projectID, "snapshots")
	ents, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read snapshots root %s: %w", root, err)
	}
	var out []snapshot.Meta
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if declared[name] {
			continue
		}
		m, err := latestMeta(cfg, projectID, name)
		if err != nil {
			return nil, err
		}
		if m == nil {
			m = &snapshot.Meta{}
		}
		m.ArchiveName = name
		m.Orphan = true
		out = append(out, *m)
	}
	return out, nil
}

// latestMeta returns the Meta for <dir>/latest, or nil if no archive
// exists for the entry.
func latestMeta(cfg identity.Config, projectID, entry string) (*snapshot.Meta, error) {
	dir := snapshot.EntryDir(cfg, projectID, entry)
	ts, err := snapshot.LatestTS(dir)
	if err != nil {
		return nil, err
	}
	if ts == "" {
		return nil, nil
	}
	m, err := snapshot.ReadMeta(dir, ts)
	if err != nil {
		return nil, err
	}
	if m.TS == "" {
		m.TS = ts
	}
	return &m, nil
}
