package render

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mtwaage/devm/internal/schema"
	"github.com/mtwaage/devm/internal/scripts"
)

// WriteDevmDir regenerates the .devm/ cache in repoRoot with current
// config values. Always overwrites — .devm/ is CLI-owned.
func WriteDevmDir(cfg schema.Config, repoRoot string) error {
	devmDir := filepath.Join(repoRoot, ".devm")
	scriptsDir := filepath.Join(devmDir, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		return fmt.Errorf("mkdir .devm/scripts: %w", err)
	}

	files := map[string]string{
		filepath.Join(devmDir, "Caddyfile"):          Caddyfile(cfg),
		filepath.Join(devmDir, "spec.yaml"):          SpecYAML(cfg, repoRoot),
		filepath.Join(scriptsDir, "init-volumes.sh"): scripts.InitVolumes,
		filepath.Join(scriptsDir, "devm-exec.sh"):    scripts.DevmExec,
	}
	for path, content := range files {
		mode := os.FileMode(0o644)
		if filepath.Ext(path) == ".sh" {
			mode = 0o755
		}
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return nil
}
