package docker

import (
	"fmt"
	"os"
	"path/filepath"
)

// writeShimBinary drops the linux/arm64 shim into the workspace's
// .devm/scripts/ so the guest can install it from the workspace mount
// during the docker-feature provisioner step.
func writeShimBinary(repoRoot string, shim []byte) error {
	dir := filepath.Join(repoRoot, ".devm", "scripts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir .devm/scripts: %w", err)
	}
	path := filepath.Join(dir, "devm-runc-shim")
	if err := os.WriteFile(path, shim, 0o755); err != nil {
		return fmt.Errorf("write devm-runc-shim: %w", err)
	}
	return nil
}
