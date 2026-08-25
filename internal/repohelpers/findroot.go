package repohelpers

import (
	"fmt"
	"os"
	"path/filepath"
)

// FindDevmYAML walks up from cwd looking for a directory containing
// devm.yaml. Returns the containing directory (the project root).
// Stops at the filesystem root and returns an error naming the
// original cwd if none found.
//
// Pure walk-up: no symlink evaluation on cwd, and no attempt to reach
// devm.yaml via a symlink chain — subsections of a devm project
// reached via a symlink (e.g. via the project's own .vm/ symlink) are
// distinct paths that the caller sees literally, and their walk-up
// terminates at whatever devm.yaml lives on that literal path.
func FindDevmYAML(cwd string) (string, error) {
	original := cwd
	dir := filepath.Clean(cwd)
	for {
		if _, err := os.Stat(filepath.Join(dir, "devm.yaml")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("not inside a devm project: no devm.yaml found in %s or any parent", original)
		}
		dir = parent
	}
}
