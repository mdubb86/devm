package helper

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Extract guarantees a runnable devm-helper binary at targetPath and
// returns targetPath on success.
//
// Idempotent by sha256: if <targetPath>.sha256 exists and its content
// matches the sha256 of the embedded gzip blob, targetPath is trusted
// and returned as-is (short-circuit — no filesystem writes). Otherwise
// the embedded blob is decompressed to a sibling tempfile, chmod'd
// 0755, renamed over targetPath atomically, and the sidecar is written.
//
// The tempfile lives in the same directory as targetPath so the rename
// is a same-filesystem operation (POSIX-atomic). A failed extraction
// leaves the tempfile behind for diagnosis and returns an error naming
// targetPath and the underlying cause.
func Extract(targetPath string) (string, error) {
	sidecar := targetPath + ".sha256"

	if fresh, _ := onDiskMatchesEmbed(sidecar); fresh {
		if _, err := os.Stat(targetPath); err == nil {
			return targetPath, nil
		}
	}

	dir := filepath.Dir(targetPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("devm-helper: create %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, "devm-helper.tmp-*")
	if err != nil {
		return "", fmt.Errorf("devm-helper: create tempfile in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	if err := decompressTo(tmp); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", fmt.Errorf("devm-helper: decompress embedded blob to %s: %w", tmpName, err)
	}
	if err := tmp.Chmod(0755); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", fmt.Errorf("devm-helper: chmod %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("devm-helper: close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, targetPath); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("devm-helper: rename %s → %s: %w", tmpName, targetPath, err)
	}
	if err := os.WriteFile(sidecar, []byte(embedSha256Hex), 0644); err != nil {
		return "", fmt.Errorf("devm-helper: write %s: %w", sidecar, err)
	}
	return targetPath, nil
}

func onDiskMatchesEmbed(sidecarPath string) (bool, error) {
	b, err := os.ReadFile(sidecarPath)
	if err != nil {
		return false, err
	}
	return string(b) == embedSha256Hex, nil
}

func decompressTo(w io.Writer) error {
	r, err := gzip.NewReader(bytes.NewReader(devmHelperGz))
	if err != nil {
		return fmt.Errorf("open gzip reader: %w", err)
	}
	defer r.Close()
	if _, err := io.Copy(w, r); err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	return nil
}
