// propose is the daemon-side entry point for "propose" requests — a
// signal that <macCwd>/<kind> changed, carrying attribution only. The
// actual bytes reach the daemon through the project's mutagen sync
// session (see devm.yaml/devm.me.yaml sync setup in vm.go); this
// handler never reads or writes the config file itself. It optionally
// re-validates the file already on disk at <macCwd>/<kind> (macCwd
// resolved from the state cache's ProjectRow) — schema.CheckUnknownKeys
// + a strict (KnownFields) decode + schema.Config.Validate, the same
// checks internal/config.Load applies to devm.yaml — and always
// records attribution to <RuntimeDir>/<projectID>/last-proposal.json.
// The Approve gate (Piece 1) fires on the next gated command as if a
// human had edited the file.
//
// Two listeners share the recorder below (recordProposal):
//
//   - A per-project HTTP listener (spawned at /vm/start) serves
//     POST /propose for the guest. Softnet forwards guest TCP
//     192.168.127.1:82 to this listener — see internal/softnet/egress.go's
//     Propose branch and this package's vm.go /vm/start wiring.
//   - The daemon's main Unix-socket mux serves
//     POST /vm/propose?project=<name> for the Mac CLI.
package serviceapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mdubb86/devm/internal/approve"
	"github.com/mdubb86/devm/internal/config"
	"github.com/mdubb86/devm/internal/daemonlog"
	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/recipes"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"gopkg.in/yaml.v3"
)

// maxProposeBodyBytes caps a propose request body. Bodies are
// metadata-only JSON now that bytes flow through mutagen sync; 1 MiB
// is generous headroom against a runaway or malicious caller.
const maxProposeBodyBytes = 1 << 20 // 1 MiB

type proposeRequest struct {
	Cwd    string `json:"cwd"`
	Branch string `json:"branch"`
	Reason string `json:"reason"`
	// Source identifies which side issued the signal: "guest" or
	// "mac". Empty defaults to "guest" — the guest binary predates
	// this field and won't send it until it's updated.
	Source string `json:"source"`
}

// proposableKinds is the ordered list of files a proposal can attribute
// to. The daemon scans every one against its last-approved snapshot on
// each propose call — the caller does not name a file.
var proposableKinds = []string{"devm.yaml", "devm.me.yaml", "devm.sh", "devm.me.sh"}

// recordProposal scans every proposable file at the project's macCwd
// (resolved from cache) against the last-approved snapshot. Files that
// diverged are attributed on last-proposal.json via WriteLastProposal;
// nothing to attribute means "no changes since last approval". If
// devm.yaml is among the changed files it's validated first — a
// malformed devm.yaml surfaces as a 400 that names the file and error,
// even when other files also changed. Missing files (mutagen sync
// hasn't landed them) are simply not in the changed set — the signal
// still records for whatever HAS landed. Returns the HTTP status +
// body callers should write, and any internal (non-4xx) error for the
// caller to log.
func recordProposal(cfg identity.Config, cache *StateCache, projectName string, req proposeRequest) (statusCode int, body string, err error) {
	row, _ := cache.ProjectRow(projectName)
	macCwd := row.MacCwd
	if macCwd == "" && req.Source == "mac" && req.Cwd != "" {
		// Mac-side propose may run before /vm/start (the cache's
		// MacCwd is empty until then). The CLI walked up cwd to find
		// devm.yaml and passes that path in req.Cwd — trust it. The
		// Unix socket is user-permission gated, so this isn't
		// arbitrary network input. Guest-side propose sends a guest
		// path in req.Cwd and cannot use this fallback.
		macCwd = req.Cwd
	}
	if macCwd == "" {
		return http.StatusPreconditionFailed,
			fmt.Sprintf("propose: project %q not started; run `devm start` from its directory first", projectName),
			nil
	}

	source := req.Source
	if source == "" {
		source = "guest"
	}

	// Guest-source propose is gated by devm.yaml's `guest.propose:`.
	// Load the currently-in-effect devm.yaml and refuse with 403 if
	// the gate is off — same behavior regardless of which files the
	// scan below finds to be changed.
	if source == "guest" {
		if yamlBytes, ferr := os.ReadFile(filepath.Join(macCwd, "devm.yaml")); ferr == nil {
			var current schema.Config
			if verr := yamlDecodeStrict(yamlBytes, &current); verr == nil {
				if !current.GuestProposeAllowed() {
					return http.StatusForbidden,
						"propose: guest.propose is disabled in devm.yaml — the Mac side is not accepting proposal signals from the guest",
						nil
				}
			}
		}
	}

	// Load the on-disk bytes for each kind once. A file missing from
	// the sync isn't an error — it just can't be in the changed set.
	onDisk := make(map[string][]byte, len(proposableKinds))
	for _, kind := range proposableKinds {
		b, readErr := os.ReadFile(filepath.Join(macCwd, kind))
		switch {
		case readErr == nil:
			onDisk[kind] = b
		case errors.Is(readErr, os.ErrNotExist):
			// Not on disk yet — sync may not have landed. Skip it
			// silently; the signal still records any other changes.
		default:
			return http.StatusInternalServerError,
				fmt.Sprintf("propose: read on-disk %s: %v", kind, readErr),
				fmt.Errorf("propose: read on-disk %s for %s: %w", kind, projectName, readErr)
		}
	}

	// Validate devm.yaml eagerly if it's present, whether or not it
	// ends up in the changed set. A schema-invalid devm.yaml is worth
	// surfacing on every propose call — a change elsewhere doesn't
	// excuse a broken yaml.
	if yamlBytes, ok := onDisk["devm.yaml"]; ok {
		if verr := schema.CheckUnknownKeys(yamlBytes); verr != nil {
			return http.StatusBadRequest, fmt.Sprintf("propose: devm.yaml: %v", verr), nil
		}
		var parsed schema.Config
		if verr := yamlDecodeStrict(yamlBytes, &parsed); verr != nil {
			return http.StatusBadRequest, fmt.Sprintf("propose: devm.yaml parse: %v", verr), nil
		}
		// Populate Functions from on-disk devm.sh + devm.me.sh before
		// Validate — otherwise validateFunctionReferences sees an
		// empty set and rejects every repos.<name>.commands /
		// services.<name>.exec entry as "not defined in devm.sh".
		// Matches config.Load's ordering (funcs then Validate).
		funcs, ferr := config.LoadFunctions(macCwd)
		if ferr != nil {
			return http.StatusInternalServerError, fmt.Sprintf("propose: load functions: %v", ferr),
				fmt.Errorf("propose: load functions for %s: %w", projectName, ferr)
		}
		parsed.Functions = funcs
		if verr := parsed.Validate(); verr != nil {
			return http.StatusBadRequest, fmt.Sprintf("propose: devm.yaml validate: %v", verr), nil
		}
	}

	// Diff each on-disk file against the approved snapshot. Kinds
	// that diverged make up the attribution set.
	changed := make([]string, 0, len(proposableKinds))
	for _, kind := range proposableKinds {
		bytesOnDisk, ok := onDisk[kind]
		if !ok {
			continue
		}
		same, err := onDiskMatchesApprovedSnapshot(cfg, projectName, kind, bytesOnDisk)
		if err != nil {
			return http.StatusInternalServerError,
				fmt.Sprintf("propose: compare %s to approved snapshot: %v", kind, err),
				fmt.Errorf("propose: compare %s for %s: %w", kind, projectName, err)
		}
		if !same {
			changed = append(changed, kind)
		}
	}

	if len(changed) == 0 {
		return http.StatusOK, "propose: no changes since last approval — nothing to review\n", nil
	}

	if werr := WriteLastProposal(cfg, projectName, ProposalMetadata{
		Cwd:       req.Cwd,
		Branch:    req.Branch,
		Reason:    req.Reason,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Source:    source,
		Kinds:     changed,
	}); werr != nil {
		return http.StatusInternalServerError, fmt.Sprintf("propose: metadata: %v", werr),
			fmt.Errorf("propose: metadata for %s: %w", projectName, werr)
	}

	return http.StatusOK, fmt.Sprintf("propose: recorded — changed: %s\n", strings.Join(changed, ", ")), nil
}

// onDiskMatchesApprovedSnapshot reports whether the on-disk bytes for
// req.Kind match the same-kind bytes in the project's last-approved
// snapshot. Returns false when no snapshot exists yet (first-run
// projects always have "changes to review" until the first approve).
func onDiskMatchesApprovedSnapshot(cfg identity.Config, projectName, kind string, onDisk []byte) (bool, error) {
	snap, hasSnap, err := approve.NewStore(cfg).Read(projectName)
	if err != nil {
		return false, err
	}
	if !hasSnap {
		return false, nil
	}
	var stored []byte
	switch kind {
	case "devm.yaml":
		stored = snap.DevmYAML
	case "devm.me.yaml":
		stored = snap.MeYAML
	case "devm.sh":
		stored = snap.DevmSH
	case "devm.me.sh":
		stored = snap.DevmMeSH
	default:
		return false, nil
	}
	return bytes.Equal(onDisk, stored), nil
}

// yamlDecodeStrict runs yaml.v3 with KnownFields(true) so any unknown
// key — top-level or nested — hard-fails with a yaml-native error.
// Mirrors internal/config/load.go's strictDecode; not shared because
// that one is unexported in a different package.
func yamlDecodeStrict(data []byte, into any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	return dec.Decode(into)
}

// decodeProposeBody reads r's body (capped at maxProposeBodyBytes)
// into a proposeRequest, writing a 400 to w on failure. The bool
// return reports whether decoding succeeded — callers stop on false.
func decodeProposeBody(w http.ResponseWriter, r *http.Request) (proposeRequest, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxProposeBodyBytes)
	var req proposeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("propose: decode body: %v", err), http.StatusBadRequest)
		return proposeRequest{}, false
	}
	return req, true
}

// writeProposalResult applies recordProposal's outcome to w, logging
// any internal error through daemonlog.Errorf first.
func writeProposalResult(w http.ResponseWriter, statusCode int, body string, err error) {
	if err != nil {
		daemonlog.Errorf("serviceapi: %v", err)
	}
	switch statusCode {
	case http.StatusNoContent:
		w.WriteHeader(statusCode)
	case http.StatusOK:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(statusCode)
		_, _ = w.Write([]byte(body))
	default:
		http.Error(w, body, statusCode)
	}
}

// handleProposeForProject returns the per-project POST /propose
// handler serving the guest side. Route registered by vm.go's
// /vm/start wiring, on the project's softnet listener.
func handleProposeForProject(cfg identity.Config, cache *StateCache, projectName string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "propose: POST only", http.StatusMethodNotAllowed)
			return
		}
		req, ok := decodeProposeBody(w, r)
		if !ok {
			return
		}
		statusCode, body, err := recordProposal(cfg, cache, projectName, req)
		writeProposalResult(w, statusCode, body, err)
	})
}

// handleProposeUnixSocket returns the daemon main-socket
// POST /vm/propose?project=<name> handler serving the Mac CLI.
// Registered by vm.go's RegisterVMHandlers.
func handleProposeUnixSocket(cfg identity.Config, cache *StateCache) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "propose: POST only", http.StatusMethodNotAllowed)
			return
		}
		projectName := r.URL.Query().Get("project")
		if projectName == "" {
			http.Error(w, "propose: project query param required", http.StatusBadRequest)
			return
		}
		req, ok := decodeProposeBody(w, r)
		if !ok {
			return
		}
		statusCode, body, err := recordProposal(cfg, cache, projectName, req)
		writeProposalResult(w, statusCode, body, err)
	})
}

// proposeListeners tracks each running project's propose HTTP listener
// so /vm/stop can close it by project name. Mirrors pop.go's
// popListeners.
var proposeListeners sync.Map // projectName -> net.Listener

// serveProposeListener runs a minimal HTTP server on ln that
// dispatches the guest-facing gdevm HTTP API. Softnet forwards guest
// TCP 192.168.127.1:82 to this listener; the endpoint path selects
// the handler:
//   - POST /propose             — edit signal.
//   - POST /passthrough         — passthrough-window request pending
//     human approval.
//   - POST /refresh-bundle      — gdevm upgrade: rebuild and re-ship
//     the provisioning bundle.
//   - GET  /recipes/list        — list recipes from the daemon's
//     cached recipes.db.
//   - GET  /recipes/get         — fetch one recipe body by name.
//   - GET  /recipes/asset/ls    — list a recipe's asset files.
//   - GET  /recipes/asset/get   — fetch one asset's raw bytes.
func serveProposeListener(ln net.Listener, cfg identity.Config, cache *StateCache, tr *tart.Tart, locks *ProjectLocks, projectName string) {
	mux := http.NewServeMux()
	mux.Handle("/propose", handleProposeForProject(cfg, cache, projectName))
	mux.Handle("/passthrough", handlePassthroughRequestForProject(cfg, cache, projectName))
	mux.Handle("/refresh-bundle", handleRefreshBundleForProject(cfg, cache, tr, locks, projectName))
	registerRecipesRoutes(mux, func() (*recipes.Query, error) {
		return recipes.Open(filepath.Join(recipes.CacheDir(), "recipes.db"))
	})
	srv := &http.Server{Handler: mux}
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
		daemonlog.Errorf("serviceapi: propose: listener for %s exited: %v", projectName, err)
	}
}

// closeProposeListener closes and forgets projectName's propose
// listener, if any. Called from /vm/stop teardown.
func closeProposeListener(projectName string) {
	if v, ok := proposeListeners.LoadAndDelete(projectName); ok {
		if ln, ok := v.(net.Listener); ok {
			ln.Close()
		}
	}
}
