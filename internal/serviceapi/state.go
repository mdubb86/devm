package serviceapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/schema"
)

// StateDir returns the directory where per-project last-applied cfg
// snapshots live. Sibling to the socket path; same lifecycle (created
// on daemon startup, wiped on uninstall).
func StateDir(cfg identity.Config) string {
	return filepath.Join(cfg.RuntimeDir(), "state")
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

// StateSnapshot is what the daemon persists per project. Wraps
// schema.Config with computed sidecar state (like rendered template
// installer content) that ComputeAllChanges needs but that isn't
// derivable from schema.Config alone at reconcile time (source files
// on disk may have changed since last apply).
//
// SecretHashes is the sha256(value) map for every `!secret NAME` ref
// this snapshot was written with. Comparing against the freshly
// resolved keychain values is how the diff engine detects
// keychain-value rotation — the ref syntax hasn't changed but the
// stored value has. Values themselves never persist to disk.
type StateSnapshot struct {
	Cfg              schema.Config     `json:"cfg"`
	TemplateContents map[string]string `json:"template_contents,omitempty"` // basename -> rendered installer content
	SecretHashes     map[string]string `json:"secret_hashes,omitempty"`     // secret name -> hex sha256(resolved value)

	// ProxyVersion is ironproxy.EmbeddedSha256() as of the last time this
	// project's iron-proxy was spawned. A running proxy whose stamp differs
	// from the current EmbeddedSha256() is STALE (spawned by an older devm
	// build) and should be respawned on the current binary.
	ProxyVersion string `json:"proxy_version,omitempty"`

	// WorkspaceHostPath is the project repoRoot, stamped here so a daemon
	// restart or `devm stop` can recover which files to (un)lock — the
	// running iron-proxy config has no notion of it. It only arrives on
	// the /vm/start request (as StartVM's WorkspaceHostPath argument) and
	// on /vm/reconcile (as VMReconcileRequest.WorkspaceHostPath); without
	// this copy, those later paths have no repoRoot to work from. The
	// orchestrator's cold-start and live-reconcile snapshot writes both
	// stamp the current value.
	WorkspaceHostPath string `json:"workspace_host_path,omitempty"`

	// ProjectIP is the project's allocated 127.42/16 loopback IP, mirrored
	// here from projectInfo so a daemon restart can recover it. Empty
	// while the project is stopped; set at /vm/start, cleared at /vm/stop.
	// AdoptIronProxies reads this back into projectInfo on daemon startup.
	ProjectIP string `json:"project_ip,omitempty"`
}

// ReadStateSnapshot loads the persisted snapshot for a project. Returns
// (nil, nil) when the file is absent or malformed — reconcile treats
// both as "assume everything changed" and computes a full diff.
func ReadStateSnapshot(cfg identity.Config, projectID string) (*StateSnapshot, error) {
	if err := validProjectID(projectID); err != nil {
		return nil, err
	}
	path := filepath.Join(StateDir(cfg), projectID+".json")
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state %s: %w", path, err)
	}
	var snap StateSnapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		// Malformed → treated as missing. Log so operators can notice.
		fmt.Fprintf(os.Stderr, "state: malformed snapshot at %s: %v (treating as missing)\n", path, err)
		return nil, nil
	}
	return &snap, nil
}

// WriteStateSnapshot persists snap as the last-applied snapshot for
// projectID. Atomic via temp file + rename to survive daemon crashes
// mid-write.
func WriteStateSnapshot(cfg identity.Config, projectID string, snap StateSnapshot) error {
	if err := validProjectID(projectID); err != nil {
		return err
	}
	if err := os.MkdirAll(StateDir(cfg), 0o700); err != nil {
		return fmt.Errorf("mkdir state dir: %w", err)
	}
	body, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}
	path := filepath.Join(StateDir(cfg), projectID+".json")
	tmp, err := os.CreateTemp(StateDir(cfg), projectID+".json.*")
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
func RemoveStateCfg(cfg identity.Config, projectID string) error {
	if err := validProjectID(projectID); err != nil {
		return err
	}
	path := filepath.Join(StateDir(cfg), projectID+".json")
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
