package mutagen

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Ensure extracts the embedded mutagen binary + its agent bundle under
// <runtimeDir>/bin/ if the sidecar sha does not match the embedded
// blob. Returns the absolute path to the mutagen binary.
//
// The agent bundle (mutagen-agents.tar.gz) must live alongside the
// mutagen binary — the CLI resolves its search path relative to argv[0]
// and refuses to install a guest agent without it, so any code path
// that ships the mutagen binary must ship the bundle in the same
// directory.
func Ensure(runtimeDir string) (string, error) {
	binDir := filepath.Join(runtimeDir, "bin")
	target := filepath.Join(binDir, "mutagen")
	sidecar := target + ".sha256"
	agentsTarget := filepath.Join(binDir, "mutagen-agents.tar.gz")

	if existing, err := os.ReadFile(sidecar); err == nil {
		if string(existing) == embedSha256Hex {
			_, binErr := os.Stat(target)
			_, agentsErr := os.Stat(agentsTarget)
			if binErr == nil && agentsErr == nil {
				return target, nil
			}
		}
	}

	if err := os.MkdirAll(binDir, 0755); err != nil {
		return "", fmt.Errorf("mutagen: mkdir %s: %w", binDir, err)
	}

	tmp, err := os.CreateTemp(binDir, "mutagen.*")
	if err != nil {
		return "", fmt.Errorf("mutagen: temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := decompressTo(tmp); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Chmod(0755); err != nil {
		tmp.Close()
		return "", fmt.Errorf("mutagen: chmod: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("mutagen: close: %w", err)
	}

	if err := os.Rename(tmpPath, target); err != nil {
		return "", fmt.Errorf("mutagen: rename %s -> %s: %w", tmpPath, target, err)
	}

	agentsTmp, err := os.CreateTemp(binDir, "mutagen-agents.*")
	if err != nil {
		return "", fmt.Errorf("mutagen agents: temp: %w", err)
	}
	agentsTmpPath := agentsTmp.Name()
	defer os.Remove(agentsTmpPath)
	if _, err := agentsTmp.Write(mutagenAgentsTarGz); err != nil {
		agentsTmp.Close()
		return "", fmt.Errorf("mutagen agents: write: %w", err)
	}
	if err := agentsTmp.Chmod(0644); err != nil {
		agentsTmp.Close()
		return "", fmt.Errorf("mutagen agents: chmod: %w", err)
	}
	if err := agentsTmp.Close(); err != nil {
		return "", fmt.Errorf("mutagen agents: close: %w", err)
	}
	if err := os.Rename(agentsTmpPath, agentsTarget); err != nil {
		return "", fmt.Errorf("mutagen agents: rename: %w", err)
	}

	if err := os.WriteFile(sidecar, []byte(embedSha256Hex), 0644); err != nil {
		return "", fmt.Errorf("mutagen: sidecar: %w", err)
	}
	return target, nil
}

func decompressTo(w io.Writer) error {
	gz, err := gzip.NewReader(bytes.NewReader(mutagenGz))
	if err != nil {
		return fmt.Errorf("mutagen: gzip: %w", err)
	}
	defer gz.Close()
	if _, err := io.Copy(w, gz); err != nil {
		return fmt.Errorf("mutagen: copy: %w", err)
	}
	return nil
}
