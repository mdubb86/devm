// internal/approve/snapshot.go
// Package approve owns the per-project last-approved snapshot store
// for the devm approve gate. See docs/superpowers/specs/
// 2026-08-31-devm-approve-gate-design.md.
package approve

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mdubb86/devm/internal/identity"
)

const (
	dirName          = "approved-snapshot"
	fileDevmYAML     = "devm.yaml"
	fileMeYAML       = "devm.me.yaml"
	fileManifest     = "manifest.json"
	sourceUser       = "user"
	sourceGuest      = "guest"
)

type Snapshot struct {
	DevmYAML []byte
	MeYAML   []byte
	Manifest Manifest
}

type Manifest struct {
	Timestamp time.Time `json:"timestamp"`
	Source    string    `json:"source"`
}

type Store struct {
	cfg identity.Config
}

func NewStore(cfg identity.Config) *Store { return &Store{cfg: cfg} }

func (s *Store) dir(projectID string) string {
	return filepath.Join(s.cfg.RuntimeDir(), projectID, dirName)
}

// Read returns the approved snapshot for projectID. ok=false means
// no snapshot has been written yet (first-run territory). Errors
// are disk-io failures only.
func (s *Store) Read(projectID string) (Snapshot, bool, error) {
	if projectID == "" {
		return Snapshot{}, false, errors.New("approve: projectID must not be empty")
	}
	d := s.dir(projectID)
	manifestBytes, err := os.ReadFile(filepath.Join(d, fileManifest))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Snapshot{}, false, nil
		}
		return Snapshot{}, false, fmt.Errorf("approve: read manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(manifestBytes, &m); err != nil {
		return Snapshot{}, false, fmt.Errorf("approve: parse manifest: %w", err)
	}
	devmBytes, err := os.ReadFile(filepath.Join(d, fileDevmYAML))
	if err != nil {
		return Snapshot{}, false, fmt.Errorf("approve: read devm.yaml: %w", err)
	}
	var meBytes []byte
	if b, err := os.ReadFile(filepath.Join(d, fileMeYAML)); err == nil {
		meBytes = b
	} else if !errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, false, fmt.Errorf("approve: read devm.me.yaml: %w", err)
	}
	return Snapshot{DevmYAML: devmBytes, MeYAML: meBytes, Manifest: m}, true, nil
}

// Write atomically advances the approved snapshot. Passing meYAML=nil
// means "no devm.me.yaml on Mac side" — any prior stored copy is
// removed so a subsequent Read reflects that absence.
func (s *Store) Write(projectID string, devmYAML, meYAML []byte, source string) error {
	if projectID == "" {
		return errors.New("approve: projectID must not be empty")
	}
	if source != sourceUser && source != sourceGuest {
		return fmt.Errorf("approve: source must be %q or %q, got %q", sourceUser, sourceGuest, source)
	}
	d := s.dir(projectID)
	if err := os.MkdirAll(d, 0700); err != nil {
		return fmt.Errorf("approve: mkdir snapshot dir: %w", err)
	}
	if err := writeAtomic(filepath.Join(d, fileDevmYAML), devmYAML); err != nil {
		return err
	}
	if meYAML == nil {
		if err := os.Remove(filepath.Join(d, fileMeYAML)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("approve: remove stale me.yaml: %w", err)
		}
	} else {
		if err := writeAtomic(filepath.Join(d, fileMeYAML), meYAML); err != nil {
			return err
		}
	}
	m := Manifest{Timestamp: time.Now().UTC(), Source: source}
	manifestBytes, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("approve: marshal manifest: %w", err)
	}
	if err := writeAtomic(filepath.Join(d, fileManifest), manifestBytes); err != nil {
		return err
	}
	return nil
}

// HashFile returns the hex SHA-256 of b. nil and empty bytes hash to
// different values via a leading marker so callers can distinguish
// "file absent" from "file present but empty" via the returned hash.
func HashFile(b []byte) string {
	if b == nil {
		return "absent"
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// writeAtomic writes bytes to path via a sibling .tmp + rename.
func writeAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return fmt.Errorf("approve: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("approve: rename %s → %s: %w", tmp, path, err)
	}
	return nil
}
