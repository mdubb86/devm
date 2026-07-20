// Package sshkeys manages per-project SSH key material for devm-managed
// VMs. All state lives under serviceapi.RuntimeDir()/ssh/projects/<id>/.
// Client privkeys never leave the Mac; the guest receives only the
// pubkey and its own host key material.
package sshkeys

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/serviceapi"
)

// ProjectDir returns the per-project ssh state directory. Callers use
// this to compute paths for the ssh_config emitter (IdentityFile,
// UserKnownHostsFile).
func ProjectDir(cfg identity.Config, projectID string) string {
	return filepath.Join(serviceapi.RuntimeDir(cfg), "ssh", "projects", projectID)
}

// EnsureProjectKeypair returns the client keypair pubkey for projectID,
// generating it on first call. Idempotent — repeat calls return the
// same on-disk pubkey without regenerating.
//
// Writes id_ed25519 (0600) and id_ed25519.pub (0644).
func EnsureProjectKeypair(cfg identity.Config, projectID string) ([]byte, error) {
	if err := validProjectID(projectID); err != nil {
		return nil, err
	}
	dir := ProjectDir(cfg, projectID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", dir, err)
	}
	privPath := filepath.Join(dir, "id_ed25519")
	pubPath := filepath.Join(dir, "id_ed25519.pub")
	if _, err := os.Stat(privPath); err == nil {
		if existing, err := os.ReadFile(pubPath); err == nil {
			return existing, nil
		}
	}
	pubStr, privPEM, err := generateEd25519Pair()
	if err != nil {
		return nil, fmt.Errorf("gen client keypair: %w", err)
	}
	if err := writeSecret(privPath, privPEM); err != nil {
		return nil, fmt.Errorf("write %s: %w", privPath, err)
	}
	if err := os.WriteFile(pubPath, []byte(pubStr), 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", pubPath, err)
	}
	return []byte(pubStr), nil
}

// EnsureProjectHostKey returns the guest host keypair for the project,
// generating it on first call. Idempotent. Also writes known_hosts
// pinning HostKeyAlias devm-<name>.
//
// Writes ssh_host_ed25519_key (0600), ssh_host_ed25519_key.pub (0644),
// and known_hosts (0644, one line).
func EnsureProjectHostKey(cfg identity.Config, name string) (priv, pub []byte, err error) {
	if err := validProjectID(name); err != nil {
		return nil, nil, err
	}
	if strings.ContainsAny(name, " \t\n\r") {
		return nil, nil, fmt.Errorf("name %q: whitespace not allowed", name)
	}
	dir := ProjectDir(cfg, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("mkdir %s: %w", dir, err)
	}
	privPath := filepath.Join(dir, "ssh_host_ed25519_key")
	pubPath := filepath.Join(dir, "ssh_host_ed25519_key.pub")
	knownPath := filepath.Join(dir, "known_hosts")
	if p, err := os.ReadFile(privPath); err == nil {
		q, err2 := os.ReadFile(pubPath)
		if err2 == nil {
			// Idempotent path: verify known_hosts is still present and correct.
			wantLine := "devm-" + name + " " + strings.TrimSpace(string(q)) + "\n"
			if existing, err := os.ReadFile(knownPath); err != nil || string(existing) != wantLine {
				if err := os.WriteFile(knownPath, []byte(wantLine), 0o644); err != nil {
					return nil, nil, fmt.Errorf("write %s: %w", knownPath, err)
				}
			}
			return p, q, nil
		}
	}
	pubStr, privPEM, err := generateEd25519Pair()
	if err != nil {
		return nil, nil, fmt.Errorf("gen host key: %w", err)
	}
	if err := writeSecret(privPath, privPEM); err != nil {
		return nil, nil, fmt.Errorf("write %s: %w", privPath, err)
	}
	if err := os.WriteFile(pubPath, []byte(pubStr), 0o644); err != nil {
		return nil, nil, fmt.Errorf("write %s: %w", pubPath, err)
	}
	// known_hosts: single line pinning HostKeyAlias devm-<name>.
	knownLine := "devm-" + name + " " + strings.TrimSpace(pubStr) + "\n"
	if err := os.WriteFile(knownPath, []byte(knownLine), 0o644); err != nil {
		return nil, nil, fmt.Errorf("write %s: %w", knownPath, err)
	}
	return []byte(privPEM), []byte(pubStr), nil
}

// Remove wipes the project's ssh subtree. Idempotent.
func Remove(cfg identity.Config, projectID string) error {
	if err := validProjectID(projectID); err != nil {
		return err
	}
	return os.RemoveAll(ProjectDir(cfg, projectID))
}

// generateEd25519Pair returns (openssh-authorized-key-format pubkey,
// PEM-encoded OpenSSH-format privkey).
func generateEd25519Pair() (pubStr, privPEM string, err error) {
	pubEd, privEd, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	sshPub, err := ssh.NewPublicKey(pubEd)
	if err != nil {
		return "", "", err
	}
	pubStr = strings.TrimRight(string(ssh.MarshalAuthorizedKey(sshPub)), "\n") + "\n"
	block, err := ssh.MarshalPrivateKey(privEd, "")
	if err != nil {
		return "", "", err
	}
	privPEM = string(pem.EncodeToMemory(block))
	return pubStr, privPEM, nil
}

func writeSecret(path string, contents string) error {
	return os.WriteFile(path, []byte(contents), 0o600)
}

// validProjectID rejects IDs that could escape ProjectDir via traversal
// or backslashes. Mirrors the discipline in serviceapi/state.go.
func validProjectID(id string) error {
	if id == "" {
		return fmt.Errorf("project id is empty")
	}
	if strings.ContainsAny(id, "/\\") || strings.Contains(id, "..") {
		return fmt.Errorf("project id %q contains illegal characters", id)
	}
	return nil
}
