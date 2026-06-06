package render

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mtwaage/devm/internal/sandbox"
	"github.com/mtwaage/devm/internal/schema"
)

// WriteDevmEnv renders cfg into repoRoot/.devm/.env via tmpfile +
// rename so a concurrent source by the with-devm-env wrapper cannot
// observe a half-written file. Creates .devm/ if missing.
//
// Idempotent: a second call with the same cfg produces an identical file.
//
// Called from the render write path AND from apply_live on KindEnv* changes.
func WriteDevmEnv(cfg schema.Config, repoRoot string) error {
	dir := filepath.Join(repoRoot, ".devm")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	final := filepath.Join(dir, ".env")
	content := sandbox.PersistentEnv(cfg)

	tmp, err := os.CreateTemp(dir, ".env.*.tmp")
	if err != nil {
		return fmt.Errorf("create tmpfile in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	// On any failure after this point, best-effort remove the tmpfile.
	cleanup := func() { _ = os.Remove(tmpPath) }

	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write tmpfile: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close tmpfile: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		cleanup()
		return fmt.Errorf("chmod tmpfile: %w", err)
	}
	if err := os.Rename(tmpPath, final); err != nil {
		cleanup()
		return fmt.Errorf("rename %s -> %s: %w", tmpPath, final, err)
	}
	return nil
}
