package serviceapi

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/tartmutagenssh"
)

// EnsureTartMutagenSSH writes the embedded tart-mutagen-ssh shim to
// <runtime-dir>/mutagen-ssh-dir/{ssh,scp} (mode 0755) and returns the
// directory path. Idempotent: skips writes when on-disk bytes already
// match the embedded bytes.
//
// The mutagen daemon's spawn env sets MUTAGEN_SSH_PATH to this directory;
// mutagen's own binary lookup finds `ssh` here in place of the system
// ssh.
func EnsureTartMutagenSSH(cfg identity.Config) (string, error) {
	dir := MutagenSSHDir(cfg)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("ensure tart-mutagen-ssh dir: %w", err)
	}
	want := tartmutagenssh.Bytes()
	wantSum := sha256.Sum256(want)
	for _, name := range []string{"ssh", "scp"} {
		p := filepath.Join(dir, name)
		if body, err := os.ReadFile(p); err == nil {
			if sha256.Sum256(body) == wantSum {
				continue
			}
		}
		if err := writeAtomic(p, want, 0o755); err != nil {
			return "", fmt.Errorf("ensure tart-mutagen-ssh %s: %w", name, err)
		}
	}
	return dir, nil
}

// writeAtomic writes to tmp+rename so a partial write can't leave a
// half-written binary in place under a crash.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
