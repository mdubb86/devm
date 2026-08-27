package mutagen

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Ensure extracts the embedded mutagen binary under
// <runtimeDir>/bin/mutagen if the sidecar sha does not match the
// embedded blob. Returns the absolute path.
func Ensure(runtimeDir string) (string, error) {
	binDir := filepath.Join(runtimeDir, "bin")
	target := filepath.Join(binDir, "mutagen")
	sidecar := target + ".sha256"

	if existing, err := os.ReadFile(sidecar); err == nil {
		if string(existing) == embedSha256Hex {
			if _, err := os.Stat(target); err == nil {
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
