package serviceapi

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mdubb86/devm/internal/identity"
)

// projectVolumesDir returns the parent Mac-side dir for one project's
// volumes: <cfg.RuntimeDir>/volumes/<projectID>/. The daemon creates
// per-volume subdirs on demand via ensureVolumeMacDir.
func projectVolumesDir(cfg identity.Config, projectID string) string {
	return filepath.Join(cfg.RuntimeDir(), "volumes", projectID)
}

// volumeMacDir returns the Mac-side path for one specific volume.
func volumeMacDir(cfg identity.Config, projectID, volumeName string) string {
	return filepath.Join(projectVolumesDir(cfg, projectID), volumeName)
}

// ensureVolumeMacDir mkdirs the Mac-side dir for a volume with mode
// 0700 and returns (path, wasEmpty, err). wasEmpty reflects the
// dir's state at observation time — before any guest boot — and is
// what the adopt logic keys off of to decide whether to copy target
// content into the volume.
//
// wasEmpty is true iff the dir contains no entries after creation
// (so a fresh mkdir returns true; a dir with any file/dir returns
// false).
func ensureVolumeMacDir(cfg identity.Config, projectID, volumeName string) (string, bool, error) {
	path := volumeMacDir(cfg, projectID, volumeName)
	if err := os.MkdirAll(path, 0700); err != nil {
		return "", false, fmt.Errorf("mkdir volume dir %s: %w", path, err)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return "", false, fmt.Errorf("read volume dir %s: %w", path, err)
	}
	return path, len(entries) == 0, nil
}
