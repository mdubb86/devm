package snapshot

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/mdubb86/devm/internal/identity"
)

// Clear removes every archive and companion file under the entry's
// snapshot directory, then removes the now-empty entry directory
// itself. It is the "start fresh" primitive operators reach for when
// the yaml declaration has drifted from the archive on disk (mode
// flipped, path renamed) or when the directory holds orphaned archives
// for an entry no longer declared in devm.yaml.
//
// Files removed: every <ts>.zst archive, every <ts>.meta.json sidecar,
// the latest symlink, failures.log, and any leftover *.tmp from a
// crashed write. The entry directory is then removed with os.Remove so
// no empty directory is left to register as an orphan; if an
// unexpected file is still present the remove fails loudly rather than
// silently leaving a dir behind.
//
// The returned count is the number of *.zst archives removed; the
// handler surfaces it to the operator as "cleared N archive(s)". A
// missing entry directory is a no-op (returns 0, nil), so Clear is
// safe to call for an entry that has never captured.
func Clear(ident identity.Config, projectID, entryName string) (int, error) {
	return clearDir(EntryDir(ident, projectID, entryName))
}

// clearDir is the directory-scoped implementation Clear wraps. Exported
// only for the unit test — the handler and CLI go through Clear so the
// RuntimeDir / projectID / entryName resolution is in one place.
func clearDir(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("read snapshot dir %s: %w", dir, err)
	}
	removed := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		path := filepath.Join(dir, name)
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return removed, fmt.Errorf("remove %s: %w", path, err)
		}
		if filepath.Ext(name) == ".zst" {
			removed++
		}
	}
	// Drop the entry dir itself so DiscoverOrphans does not surface
	// it as an orphan after the sweep. os.Remove (not RemoveAll) so an
	// unexpected leftover errors out loudly rather than being wiped.
	if err := os.Remove(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return removed, fmt.Errorf("remove %s: %w", dir, err)
	}
	return removed, nil
}
