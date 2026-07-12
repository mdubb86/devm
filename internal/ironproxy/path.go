package ironproxy

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Ensure guarantees a runnable iron-proxy binary at
// <runtimeDir>/bin/iron-proxy and returns its absolute path.
//
// Idempotent by sha256: if <runtimeDir>/bin/iron-proxy.sha256 exists
// and its content matches the sha256 of the embedded gzip blob, the
// on-disk iron-proxy is trusted and returned as-is. Otherwise the
// embedded blob is decompressed to a sibling tempfile, chmod'd 0755,
// and renamed over the target atomically.
//
// The tempfile lives in the same directory as the target so the
// rename is a same-filesystem operation (POSIX-atomic). A failed
// extraction leaves the tempfile behind for diagnosis, and returns
// an error naming the destination path and the underlying cause.
func Ensure(runtimeDir string) (string, error) {
	binDir := filepath.Join(runtimeDir, "bin")
	target := filepath.Join(binDir, "iron-proxy")
	sidecar := target + ".sha256"

	if fresh, _ := onDiskMatchesEmbed(sidecar); fresh {
		if _, err := os.Stat(target); err == nil {
			return target, nil
		}
	}

	if err := os.MkdirAll(binDir, 0755); err != nil {
		return "", fmt.Errorf("iron-proxy: create %s: %w", binDir, err)
	}

	tmp, err := os.CreateTemp(binDir, "iron-proxy.tmp-*")
	if err != nil {
		return "", fmt.Errorf("iron-proxy: create tempfile in %s: %w", binDir, err)
	}
	tmpName := tmp.Name()
	if err := decompressTo(tmp); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", fmt.Errorf("iron-proxy: decompress embedded blob to %s: %w", tmpName, err)
	}
	if err := tmp.Chmod(0755); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", fmt.Errorf("iron-proxy: chmod %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("iron-proxy: close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("iron-proxy: rename %s → %s: %w", tmpName, target, err)
	}
	if err := os.WriteFile(sidecar, []byte(embedSha256Hex), 0644); err != nil {
		return "", fmt.Errorf("iron-proxy: write %s: %w", sidecar, err)
	}
	return target, nil
}

func onDiskMatchesEmbed(sidecarPath string) (bool, error) {
	b, err := os.ReadFile(sidecarPath)
	if err != nil {
		return false, err
	}
	return string(b) == embedSha256Hex, nil
}

func decompressTo(w io.Writer) error {
	r, err := gzip.NewReader(bytes.NewReader(ironProxyGz))
	if err != nil {
		return fmt.Errorf("open gzip reader: %w", err)
	}
	defer r.Close()
	if _, err := io.Copy(w, r); err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	return nil
}
