package serviceapi

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mdubb86/devm/internal/identity"
)

// projectMirrorRoot returns the parent Mac-side dir for one project's
// mirrors: <cfg.RuntimeDir>/<projectID>/. The daemon creates
// per-label subdirs on demand via ensureMirrorDir.
func projectMirrorRoot(cfg identity.Config, projectID string) string {
	return filepath.Join(cfg.RuntimeDir(), projectID)
}

// mirrorMacDir returns the Mac-side path for one specific mirror.
func mirrorMacDir(cfg identity.Config, projectID, label string) string {
	return filepath.Join(projectMirrorRoot(cfg, projectID), label)
}

// ensureMirrorDir mkdirs the Mac-side dir for a mirror with mode
// 0700 and returns (path, wasEmpty, err). wasEmpty reflects the
// dir's state at observation time — before any guest boot — and is
// what the adopt logic keys off of to decide whether to copy target
// content into the mirror.
//
// wasEmpty is true iff the dir contains no entries after creation
// (so a fresh mkdir returns true; a dir with any file/dir returns
// false).
func ensureMirrorDir(cfg identity.Config, projectID, label string) (string, bool, error) {
	path := mirrorMacDir(cfg, projectID, label)
	if err := os.MkdirAll(path, 0700); err != nil {
		return "", false, fmt.Errorf("mkdir mirror dir %s: %w", path, err)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return "", false, fmt.Errorf("read mirror dir %s: %w", path, err)
	}
	return path, len(entries) == 0, nil
}
