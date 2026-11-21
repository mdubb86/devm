package serviceapi

import (
	"os"
	"path/filepath"

	"github.com/mdubb86/devm/internal/identity"
)

// PopScratchRoot returns <RuntimeDir>/pop-tmp/ — the flat root of
// one-file-per-pop cp targets `devm pop --native` writes to for a
// guest path that isn't inside any mirror (see cmd/devm/pop.go's
// runPopNative). Populated via `tart exec cat`, one file per pop;
// wiped on daemon boot.
func PopScratchRoot(cfg identity.Config) string {
	return filepath.Join(cfg.RuntimeDir(), "pop-tmp")
}

// WipePopScratchOnStartup removes the daemon-owned scratch dir at
// boot. The dir holds one-shot cp targets from `devm pop --native`
// against out-of-mirror files; wiping at daemon boot is fine since
// no user is expected to persist those files across a daemon restart.
func WipePopScratchOnStartup(cfg identity.Config) error {
	if err := os.RemoveAll(PopScratchRoot(cfg)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
