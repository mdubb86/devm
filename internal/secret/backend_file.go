package secret

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// NewFileBackend returns a Backend that stores each secret as a
// mode-0600 file under root. Layout: <root>/<projectID>/<keyName>.
// The root is created lazily on the first Set with mode 0700.
//
// Accounts follow the same "projectID/keyName" convention the macOS
// keychain backend used, so no caller changes; the file backend just
// translates the "/" into a nested directory. Accounts without a "/"
// land at the root, treated as globals for the purpose of List (they
// won't show up under any projectID prefix).
func NewFileBackend(root string) Backend {
	return &fileBackend{root: root}
}

type fileBackend struct {
	root string
}

func (f *fileBackend) Set(account, value string) error {
	if err := validateAccount(account); err != nil {
		return err
	}
	path := f.pathFor(account)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("secret: mkdir %s: %w", filepath.Dir(path), err)
	}
	// Atomic write: temp in the same dir, then rename. Keeps a partial
	// value from ever being visible if the process is killed mid-write.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("secret: create temp for %s: %w", account, err)
	}
	tmpPath := tmp.Name()
	// Best-effort cleanup on any failure path below.
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("secret: chmod temp for %s: %w", account, err)
	}
	if _, err := tmp.WriteString(value); err != nil {
		tmp.Close()
		return fmt.Errorf("secret: write temp for %s: %w", account, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("secret: close temp for %s: %w", account, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("secret: rename temp for %s: %w", account, err)
	}
	return nil
}

func (f *fileBackend) Get(account string) (string, error) {
	if err := validateAccount(account); err != nil {
		return "", err
	}
	b, err := os.ReadFile(f.pathFor(account))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("secret: read %s: %w", account, err)
	}
	return string(b), nil
}

func (f *fileBackend) List(projectID string) ([]string, error) {
	if err := validateAccountSegment(projectID); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(f.root, projectID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("secret: list %s: %w", projectID, err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// Skip the atomic-write scratch files if a Set was interrupted.
		if strings.HasPrefix(name, ".tmp-") {
			continue
		}
		out = append(out, name)
	}
	return out, nil
}

func (f *fileBackend) Delete(account string) error {
	if err := validateAccount(account); err != nil {
		return err
	}
	err := os.Remove(f.pathFor(account))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrNotFound
		}
		return fmt.Errorf("secret: delete %s: %w", account, err)
	}
	return nil
}

func (f *fileBackend) pathFor(account string) string {
	// filepath.Join collapses ".." and cleans separators — the
	// validateAccount call above already rejects such names, so this
	// just resolves the split.
	return filepath.Join(f.root, account)
}

// validateAccount rejects account names that would escape the root
// dir or produce a hidden entry that List's .tmp- filter drops.
// Every "/"-separated segment goes through validateAccountSegment.
func validateAccount(account string) error {
	if account == "" {
		return fmt.Errorf("secret: account name is empty")
	}
	for _, seg := range strings.Split(account, "/") {
		if err := validateAccountSegment(seg); err != nil {
			return err
		}
	}
	return nil
}

func validateAccountSegment(seg string) error {
	if seg == "" {
		return fmt.Errorf("secret: empty segment in account")
	}
	if seg == "." || seg == ".." {
		return fmt.Errorf("secret: account segment %q would escape the secrets dir", seg)
	}
	if strings.ContainsAny(seg, `/\` + "\x00") {
		return fmt.Errorf("secret: account segment %q contains invalid characters", seg)
	}
	if strings.HasPrefix(seg, ".tmp-") {
		return fmt.Errorf("secret: account segment %q collides with the atomic-write scratch prefix", seg)
	}
	return nil
}
