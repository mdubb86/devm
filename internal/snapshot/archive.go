package snapshot

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mdubb86/devm/internal/identity"
)

// timestampLayout is the devm snapshot archive filename stamp — a
// sortable UTC instant with no colons (NTFS/HFS-neutral) and a dash
// before seconds to disambiguate from the hour-minute block.
const timestampLayout = "2006-01-02T1504-05"

// NewTimestamp renders an instant as the filename stamp. The instant
// is converted to UTC first so callers in other zones still produce
// a sortable value.
func NewTimestamp(now time.Time) string {
	return now.UTC().Format(timestampLayout)
}

// EnsureEntryDir mkdirs the per-entry directory with 0755.
func EnsureEntryDir(ident identity.Config, projectID, entryName string) error {
	dir := EntryDir(ident, projectID, entryName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir snapshot entry dir %s: %w", dir, err)
	}
	return nil
}

// WriteMeta encodes m as JSON and writes <dir>/<ts>.meta.json
// atomically (temp + rename). The archive's own fsync (upstream)
// orders the surrounding write; losing the sidecar on crash is
// tolerable because ReadMeta returns SourceUnknown on missing.
func WriteMeta(dir, ts string, m Meta) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir meta dir %s: %w", dir, err)
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal meta: %w", err)
	}
	b = append(b, '\n')
	final := filepath.Join(dir, ts+".meta.json")
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return fmt.Errorf("write meta tmp: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return fmt.Errorf("rename meta: %w", err)
	}
	return nil
}

// ReadMeta returns the sidecar for <ts>. When the sidecar is missing
// the archive may still exist; return Meta{Source: SourceUnknown},
// nil so the caller can decide how to proceed.
func ReadMeta(dir, ts string) (Meta, error) {
	b, err := os.ReadFile(filepath.Join(dir, ts+".meta.json"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Meta{Source: SourceUnknown}, nil
		}
		return Meta{}, fmt.Errorf("read meta: %w", err)
	}
	var m Meta
	if err := json.Unmarshal(b, &m); err != nil {
		return Meta{}, fmt.Errorf("unmarshal meta: %w", err)
	}
	// v0.31 sidecars have no "status" field; normalize the zero
	// value on read so downstream code never has to branch on "" vs
	// "ok". Mode stays empty on disk — EffectiveMode() resolves it.
	if m.Status == "" {
		m.Status = StatusOk
	}
	return m, nil
}

// UpdateLatest points <dir>/latest at <ts>.zst via symlink,
// atomically (symlink to a temp name + rename). The temp name
// avoids a race between two concurrent updaters; upstream callers
// hold the flock during lifecycle writes, but belt-and-suspenders
// here is cheap.
func UpdateLatest(dir, ts string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir latest dir %s: %w", dir, err)
	}
	final := filepath.Join(dir, "latest")
	tmp := filepath.Join(dir, "latest.tmp")
	// Clear stale tmp from a prior crash so Symlink does not EEXIST.
	_ = os.Remove(tmp)
	if err := os.Symlink(ts+".zst", tmp); err != nil {
		return fmt.Errorf("symlink latest.tmp: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename latest: %w", err)
	}
	return nil
}

// ListTimestamps returns every archive's <ts> in dir, parsed from
// <ts>.zst filenames. The list is deterministic by timestamp sort
// ordering (ascending — callers sort by the on-wire shape they want).
// A stray *.zst whose stem doesn't parse as the archive timestamp is
// skipped, matching Prune's rule: a non-stamp file is not an archive
// and cannot be counted among them. A missing dir is an empty slice.
func ListTimestamps(dir string) ([]string, error) {
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
		if _, err := time.Parse(timestampLayout, stem); err != nil {
			continue
		}
		stamps = append(stamps, stem)
	}
	return stamps, nil
}

// SelectLatest returns the ts the latest symlink should point at:
// the newest archive whose Meta.Status is StatusOk, falling back to
// the newest StatusPostFailed only when no ok archive exists.
// Returns ("", nil) on empty or missing dirs.
//
// The symlink is an operator's default-restore handle; a flagged
// archive is reachable by its own ts but never promoted to the
// default. ReadMeta normalizes a v0.31 sidecar's empty Status to
// StatusOk, so legacy archives count as ok without special-case code.
func SelectLatest(dir string) (string, error) {
	stamps, err := ListTimestamps(dir)
	if err != nil {
		return "", err
	}
	if len(stamps) == 0 {
		return "", nil
	}
	sort.Sort(sort.Reverse(sort.StringSlice(stamps)))
	var newestPostFailed string
	for _, ts := range stamps {
		m, err := ReadMeta(dir, ts)
		if err != nil {
			return "", fmt.Errorf("select latest: read meta %s: %w", ts, err)
		}
		if m.Status == StatusOk {
			return ts, nil
		}
		if m.Status == StatusPostFailed && newestPostFailed == "" {
			newestPostFailed = ts
		}
	}
	return newestPostFailed, nil
}

// LatestTS reads <dir>/latest and returns the <ts> portion of its
// target (basename minus the ".zst" suffix). When the symlink is
// missing or its target archive is gone, returns "", nil; the
// caller decides whether that is first-run or operator intervention.
func LatestTS(dir string) (string, error) {
	link := filepath.Join(dir, "latest")
	target, err := os.Readlink(link)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("readlink latest: %w", err)
	}
	// Target is a relative name alongside the symlink; resolve it
	// to check the archive still exists.
	if !filepath.IsAbs(target) {
		target = filepath.Join(dir, target)
	}
	if _, err := os.Stat(target); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("stat latest target: %w", err)
	}
	base := filepath.Base(target)
	return strings.TrimSuffix(base, ".zst"), nil
}
