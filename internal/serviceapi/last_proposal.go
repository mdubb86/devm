// last_proposal.go — per-project storage for the most recent
// guest-originated propose call. Written by POST /propose, read by
// GET /vm/approve-state, cleared by POST /vm/approve.
package serviceapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mdubb86/devm/internal/identity"
)

// ProposalMetadata is the attribution ribbon a guest-originated
// propose call ships to the Mac side. It lives on disk at
// <RuntimeDir>/<projectID>/last-proposal.json; consumers read it
// through ReadLastProposal.
type ProposalMetadata struct {
	Cwd       string `json:"cwd"`
	Branch    string `json:"branch"`
	Reason    string `json:"reason"`
	Timestamp string `json:"timestamp"`
	Source    string `json:"source"`
	Kind      string `json:"kind"`
}

func lastProposalPath(cfg identity.Config, projectID string) string {
	return filepath.Join(cfg.RuntimeDir(), projectID, "last-proposal.json")
}

// WriteLastProposal writes m to the project's last-proposal.json,
// atomic tmp+rename. Overwrites any prior file.
func WriteLastProposal(cfg identity.Config, projectID string, m ProposalMetadata) error {
	path := lastProposalPath(cfg, projectID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("last-proposal mkdir: %w", err)
	}
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("last-proposal marshal: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return fmt.Errorf("last-proposal write tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("last-proposal rename: %w", err)
	}
	return nil
}

// ReadLastProposal returns the project's stored proposal metadata
// if any. (nil, false, nil) when the file is absent.
func ReadLastProposal(cfg identity.Config, projectID string) (*ProposalMetadata, bool, error) {
	body, err := os.ReadFile(lastProposalPath(cfg, projectID))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("last-proposal read: %w", err)
	}
	var m ProposalMetadata
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, false, fmt.Errorf("last-proposal unmarshal: %w", err)
	}
	return &m, true, nil
}

// ClearLastProposal removes the project's last-proposal.json. A
// missing file is not an error — the caller (POST /vm/approve) has
// no way to know whether a proposal was ever recorded.
func ClearLastProposal(cfg identity.Config, projectID string) error {
	err := os.Remove(lastProposalPath(cfg, projectID))
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("last-proposal remove: %w", err)
}
