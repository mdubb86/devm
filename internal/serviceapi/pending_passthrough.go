// pending_passthrough.go — per-project storage for a passthrough
// window a guest agent (`gdevm passthrough --reason ...`) requested
// and that a human on the Mac side has not yet approved or denied.
//
// Only one pending request per project at a time. A second guest
// request replaces the first — most-recent wins.
package serviceapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mdubb86/devm/internal/identity"
)

// PendingPassthroughRequest is the record a guest passthrough request
// leaves on disk while waiting for Mac-side approval. Lives at
// <RuntimeDir>/<projectID>/pending-passthrough.json.
type PendingPassthroughRequest struct {
	// Reason is the human-readable justification the guest supplied.
	// The Mac reviewer sees this verbatim.
	Reason string `json:"reason"`
	// DurationSeconds is the window length the guest asked for. The
	// guest CLI requires this to be positive — a pending record with
	// 0 here is either from a pre-required-duration gdevm binary
	// still on disk, or malformed; the approve handler refuses it.
	DurationSeconds int `json:"duration_seconds"`
	// Cwd is the guest cwd at request time — attribution only.
	Cwd string `json:"cwd,omitempty"`
	// Branch is `git symbolic-ref --short HEAD` at request time —
	// attribution only.
	Branch string `json:"branch,omitempty"`
	// Timestamp is when the daemon recorded the request (RFC3339 UTC).
	Timestamp string `json:"timestamp"`
	// Source records who submitted the request. Always "guest" in
	// practice today; kept as a field for future symmetry with
	// last-proposal.json.
	Source string `json:"source"`
}

func pendingPassthroughPath(cfg identity.Config, projectID string) string {
	return filepath.Join(cfg.RuntimeDir(), projectID, "pending-passthrough.json")
}

// WritePendingPassthrough atomically stores req at the project's
// pending-passthrough.json. Overwrites any prior request — the guest
// can update the reason or duration by submitting again.
func WritePendingPassthrough(cfg identity.Config, projectID string, req PendingPassthroughRequest) error {
	path := pendingPassthroughPath(cfg, projectID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("pending-passthrough mkdir: %w", err)
	}
	body, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return fmt.Errorf("pending-passthrough marshal: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return fmt.Errorf("pending-passthrough write tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("pending-passthrough rename: %w", err)
	}
	return nil
}

// ReadPendingPassthrough returns the current pending request if any.
// (nil, false, nil) when no request is queued.
func ReadPendingPassthrough(cfg identity.Config, projectID string) (*PendingPassthroughRequest, bool, error) {
	body, err := os.ReadFile(pendingPassthroughPath(cfg, projectID))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("pending-passthrough read: %w", err)
	}
	var req PendingPassthroughRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, false, fmt.Errorf("pending-passthrough unmarshal: %w", err)
	}
	return &req, true, nil
}

// ClearPendingPassthrough removes the project's pending-passthrough.json.
// A missing file is not an error.
func ClearPendingPassthrough(cfg identity.Config, projectID string) error {
	err := os.Remove(pendingPassthroughPath(cfg, projectID))
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("pending-passthrough remove: %w", err)
}
