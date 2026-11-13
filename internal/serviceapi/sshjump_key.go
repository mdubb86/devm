package serviceapi

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
)

const jumpHostKeyName = "jump-host-key"

// LoadOrCreateJumpHostKey returns the daemon-scope ed25519 SSH host
// key for devm-jump, generating + persisting it on first call. The
// key sits at <runtimeDir>/ssh/jump-host-key (0600) so the guest's
// known_hosts pin survives daemon restarts.
func LoadOrCreateJumpHostKey(runtimeDir string) (ssh.Signer, error) {
	dir := filepath.Join(runtimeDir, "ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("ssh host key dir: %w", err)
	}
	path := filepath.Join(dir, jumpHostKeyName)
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		_, priv, gerr := ed25519.GenerateKey(rand.Reader)
		if gerr != nil {
			return nil, fmt.Errorf("generate jump host key: %w", gerr)
		}
		pemBytes, perr := ssh.MarshalPrivateKey(priv, "devm-jump host key")
		if perr != nil {
			return nil, fmt.Errorf("marshal jump host key: %w", perr)
		}
		encoded := pem.EncodeToMemory(pemBytes)
		if werr := os.WriteFile(path, encoded, 0o600); werr != nil {
			return nil, fmt.Errorf("write jump host key: %w", werr)
		}
		return ssh.NewSignerFromKey(priv)
	}
	if err != nil {
		return nil, fmt.Errorf("read jump host key: %w", err)
	}
	return ssh.ParsePrivateKey(body)
}

// LoadOrCreateProjectJumpKey returns the per-project ed25519 client
// key for guest→devm-jump auth, generating + persisting on first
// call. Stored at <runtimeDir>/ssh/<projectID>.jump.{priv,pub}.
// Privkey bytes are OpenSSH format so the guest can drop them into
// ~/.ssh/devm-jump-id verbatim.
func LoadOrCreateProjectJumpKey(runtimeDir, projectID string) ([]byte, ssh.PublicKey, error) {
	dir := filepath.Join(runtimeDir, "ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("ssh key dir: %w", err)
	}
	privPath := filepath.Join(dir, projectID+".jump.priv")
	pubPath := filepath.Join(dir, projectID+".jump.pub")
	privBytes, err := os.ReadFile(privPath)
	if errors.Is(err, os.ErrNotExist) {
		pub, priv, gerr := ed25519.GenerateKey(rand.Reader)
		if gerr != nil {
			return nil, nil, fmt.Errorf("generate jump client key: %w", gerr)
		}
		pemBytes, perr := ssh.MarshalPrivateKey(priv, "devm-jump client key for "+projectID)
		if perr != nil {
			return nil, nil, fmt.Errorf("marshal jump client key: %w", perr)
		}
		encoded := pem.EncodeToMemory(pemBytes)
		if werr := os.WriteFile(privPath, encoded, 0o600); werr != nil {
			return nil, nil, fmt.Errorf("write jump client key: %w", werr)
		}
		sshPub, perr2 := ssh.NewPublicKey(pub)
		if perr2 != nil {
			return nil, nil, fmt.Errorf("marshal jump client pub: %w", perr2)
		}
		if werr := os.WriteFile(pubPath, ssh.MarshalAuthorizedKey(sshPub), 0o644); werr != nil {
			return nil, nil, fmt.Errorf("write jump client pub: %w", werr)
		}
		return encoded, sshPub, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read jump client key: %w", err)
	}
	signer, perr := ssh.ParsePrivateKey(privBytes)
	if perr != nil {
		return nil, nil, fmt.Errorf("parse jump client key: %w", perr)
	}
	return privBytes, signer.PublicKey(), nil
}
