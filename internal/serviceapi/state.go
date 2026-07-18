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
// on daemon startup, wiped on uninstall). Follows the same
// $DEVM_RUNTIME_DIR override as SocketPath so an e2e-sandboxed daemon
// keeps its state alongside its socket.
func StateDir() string {
	return filepath.Join(RuntimeDir(), "state")
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

	// SSHHostPort is the daemon-picked host port softnet forwards to the
	// guest's :22, mirrored here from ironProxyInfo so a daemon restart
	// can recover it. ironProxyInfo itself is rebuilt on restart from the
	// running iron-proxy's on-disk YAML config (AdoptIronProxies), which
	// has no notion of SSH — without this copy the port would reset to 0
	// on every restart, orphaning any ssh_config already emitted with the
	// old port. recoverProjectState reads it back; the orchestrator's
	// cold-start and live-reconcile snapshot writes both stamp the
	// current value.
	SSHHostPort int `json:"ssh_host_port,omitempty"`
}

// ReadStateSnapshot loads the persisted snapshot for a project. Returns
// (nil, nil) when the file is absent or malformed — reconcile treats
// both as "assume everything changed" and computes a full diff.
func ReadStateSnapshot(projectID string) (*StateSnapshot, error) {
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
func WriteStateSnapshot(projectID string, snap StateSnapshot) error {
	if err := validProjectID(projectID); err != nil {
		return err
	}
	if err := os.MkdirAll(StateDir(), 0o700); err != nil {
		return fmt.Errorf("mkdir state dir: %w", err)
	}
	body, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
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
