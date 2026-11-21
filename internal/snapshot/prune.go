package snapshot

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Prune deletes all but the `keep` newest archives in dir. "Newest"
// is decided by the timestamp parsed out of the filename (NOT
// mtime), so the order is deterministic across copies and fs
// migrations. Each archive's paired <ts>.meta.json is removed too
// when present.
//
// keep must be >= 1 — callers never want to purge everything, so
// a non-positive value is rejected rather than quietly interpreted.
//
// A missing dir is not an error: the entry simply has not been
// captured yet.
func Prune(dir string, keep int) ([]string, error) {
	if keep <= 0 {
		return nil, fmt.Errorf("prune: keep must be positive, got %d", keep)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read snapshot dir %s: %w", dir, err)
	}
	var stamps []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".zst") {
			continue
		}
		stem := strings.TrimSuffix(name, ".zst")
		// Only treat *.zst as a snapshot if its stem parses as our
		// filename timestamp. A stray file like "garbage.zst" sorts
		// lexically ahead of real stamps and would otherwise be
		// "protected" as newest while a real archive gets pruned.
		if _, err := time.Parse(timestampLayout, stem); err != nil {
			continue
		}
		stamps = append(stamps, stem)
	}
	// Timestamp strings are chosen so lexical descending equals
	// chronological descending.
	sort.Sort(sort.Reverse(sort.StringSlice(stamps)))
	if len(stamps) <= keep {
		return nil, nil
	}
	var removed []string
	for _, ts := range stamps[keep:] {
		arc := filepath.Join(dir, ts+".zst")
		if err := os.Remove(arc); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return removed, fmt.Errorf("remove archive %s: %w", arc, err)
		}
		meta := filepath.Join(dir, ts+".meta.json")
		if err := os.Remove(meta); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return removed, fmt.Errorf("remove meta %s: %w", meta, err)
		}
		removed = append(removed, ts)
	}
	return removed, nil
}
