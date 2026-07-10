package serviceapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mdubb86/devm/internal/schema"
)

// StateDir returns the directory where per-project last-applied cfg
// snapshots live. Sibling to the socket path; same lifecycle (created
// on daemon startup, wiped on uninstall).
func StateDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Application Support", "devm", "state")
}

// validProjectID rejects project IDs that could escape StateDir() via
// filepath.Join — e.g. "../evil" or "foo/bar" — before they reach a
// filesystem call. projectID arrives over the daemon's unix-socket HTTP
// API (POST /vm/start, /vm/reconcile, ...), so it's attacker-reachable
// input, not just internal plumbing. "." alone is allowed (project IDs
// like "foo.bar" are legitimate); only path separators and ".."
// traversal are rejected.
func validProjectID(id string) error {
	if id == "" || strings.ContainsAny(id, "/\\") || strings.Contains(id, "..") {
		return fmt.Errorf("invalid project id %q", id)
	}
	return nil
}

// ReadStateCfg loads the last-applied cfg for a project. Returns
// (nil, nil) when the file is absent or malformed — reconcile treats
// both as "assume everything changed" and computes a full diff.
func ReadStateCfg(projectID string) (*schema.Config, error) {
	if err := validProjectID(projectID); err != nil {
		return nil, err
	}
	path := filepath.Join(StateDir(), projectID+".json")
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state %s: %w", path, err)
	}
	var cfg schema.Config
	if err := json.Unmarshal(body, &cfg); err != nil {
		// Malformed → treated as missing. Log so operators can notice.
		fmt.Fprintf(os.Stderr, "state: malformed snapshot at %s: %v (treating as missing)\n", path, err)
		return nil, nil
	}
	return &cfg, nil
}

// WriteStateCfg persists cfg as the last-applied snapshot. Atomic via
// temp file + rename to survive daemon crashes mid-write.
func WriteStateCfg(projectID string, cfg schema.Config) error {
	if err := validProjectID(projectID); err != nil {
		return err
	}
	if err := os.MkdirAll(StateDir(), 0o700); err != nil {
		return fmt.Errorf("mkdir state dir: %w", err)
	}
	body, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal cfg: %w", err)
	}
	path := filepath.Join(StateDir(), projectID+".json")
	tmp, err := os.CreateTemp(StateDir(), projectID+".json.*")
	if err != nil {
		return fmt.Errorf("create tmp state file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write tmp state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, path)
}

// RemoveStateCfg deletes the snapshot for projectID. Idempotent —
// remove-of-missing is not an error.
func RemoveStateCfg(projectID string) error {
	if err := validProjectID(projectID); err != nil {
		return err
	}
	path := filepath.Join(StateDir(), projectID+".json")
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
