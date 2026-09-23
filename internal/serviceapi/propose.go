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
	"sync"
	"time"

	"github.com/mdubb86/devm/internal/daemonlog"
	"github.com/mdubb86/devm/internal/identity"
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
	Kind   string `json:"kind"`
	// Source identifies which side issued the signal: "guest" or
	// "mac". Empty defaults to "guest" — the guest binary predates
	// this field and won't send it until it's updated.
	Source string `json:"source"`
}

// recordProposal validates the on-disk file for req.Kind under the
// project's macCwd, resolved from cache (skipped when the file is
// missing — sync may not have landed it yet, and a signal that
// arrives ahead of its bytes still deserves attribution), then writes
// req's attribution to last-proposal.json via WriteLastProposal.
// Returns the HTTP status and body callers should write, and any
// internal (non-4xx) error for the caller to log.
func recordProposal(cfg identity.Config, cache *StateCache, projectName string, req proposeRequest) (statusCode int, body string, err error) {
	if req.Kind != "devm.yaml" && req.Kind != "devm.me.yaml" {
		return http.StatusBadRequest, fmt.Sprintf("propose: unsupported kind %q", req.Kind), nil
	}

	row, ok := cache.ProjectRow(projectName)
	if !ok || row.MacCwd == "" {
		return http.StatusPreconditionFailed,
			fmt.Sprintf("propose: project %q not started; run `devm start` from its directory first", projectName),
			nil
	}

	// devm.me.yaml has no schema of its own (it's a partial merged
	// into devm.yaml) — nothing to validate against.
	configPath := filepath.Join(row.MacCwd, req.Kind)
	onDisk, readErr := os.ReadFile(configPath)
	switch {
	case readErr == nil:
		if req.Kind == "devm.yaml" {
			if verr := schema.CheckUnknownKeys(onDisk); verr != nil {
				return http.StatusBadRequest, fmt.Sprintf("propose: yaml: %v", verr), nil
			}
			var parsed schema.Config
			if verr := yamlDecodeStrict(onDisk, &parsed); verr != nil {
				return http.StatusBadRequest, fmt.Sprintf("propose: yaml parse: %v", verr), nil
			}
			if verr := parsed.Validate(); verr != nil {
				return http.StatusBadRequest, fmt.Sprintf("propose: yaml validate: %v", verr), nil
			}
		}
	case errors.Is(readErr, os.ErrNotExist):
		// No on-disk file yet — sync may not have landed it. Validation
		// is skipped; metadata still records the signal.
	default:
		return http.StatusInternalServerError, fmt.Sprintf("propose: read on-disk file: %v", readErr),
			fmt.Errorf("propose: read on-disk %s for %s: %w", req.Kind, projectName, readErr)
	}

	source := req.Source
	if source == "" {
		source = "guest"
	}

	if werr := WriteLastProposal(cfg, projectName, ProposalMetadata{
		Cwd:       req.Cwd,
		Branch:    req.Branch,
		Reason:    req.Reason,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Source:    source,
		Kind:      req.Kind,
	}); werr != nil {
		return http.StatusInternalServerError, fmt.Sprintf("propose: metadata: %v", werr),
			fmt.Errorf("propose: metadata for %s: %w", projectName, werr)
	}

	return http.StatusNoContent, "", nil
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
	if statusCode != http.StatusNoContent {
		http.Error(w, body, statusCode)
		return
	}
	w.WriteHeader(statusCode)
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
		// The softnet listener is bound per-project at start, so its
		// projectName is trusted — only this Unix-socket path takes an
		// arbitrary caller-supplied project query param, so only it
		// needs to check the project actually exists before recording
		// a proposal under its state dir.
		if _, err := os.Stat(stateDirForProject(cfg, projectName)); errors.Is(err, os.ErrNotExist) {
			http.Error(w, fmt.Sprintf("propose: unknown project %q", projectName), http.StatusNotFound)
			return
		} else if err != nil {
			daemonlog.Errorf("serviceapi: propose: stat state dir for %s: %v", projectName, err)
			http.Error(w, fmt.Sprintf("propose: stat state dir: %v", err), http.StatusInternalServerError)
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

// serveProposeListener runs a minimal HTTP server on ln that dispatches
// POST /propose to handleProposeForProject for the given project.
func serveProposeListener(ln net.Listener, cfg identity.Config, cache *StateCache, projectName string) {
	mux := http.NewServeMux()
	mux.Handle("/propose", handleProposeForProject(cfg, cache, projectName))
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
