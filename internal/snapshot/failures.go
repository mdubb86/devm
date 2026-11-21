package snapshot

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// failures.log is the per-entry failure ring. Each line is a JSON
// object with at least {"ts", "source", "exit_code", "reason"}.
// AppendFailure writes verbatim; the sender decides on the full
// shape. The reason field may contain newlines — json encoding
// escapes them, so each failure stays a single line.
//
// When the file grows beyond maxFailuresBytes, the oldest lines are
// trimmed off so the kept tail fits in trimTargetBytes. Newest
// lines live at the bottom.
const (
	failuresFile      = "failures.log"
	maxFailuresBytes  = 4096
	trimTargetBytes   = 1024
)

// AppendFailure appends line to the failures ring. The line is
// written with exactly one trailing newline (a caller-supplied
// trailing newline is accepted but not duplicated). When the file
// exceeds the rotate threshold, the ring is trimmed to the target.
func AppendFailure(dir, line string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir failures dir %s: %w", dir, err)
	}
	line = strings.TrimRight(line, "\n") + "\n"
	path := filepath.Join(dir, failuresFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open failures log: %w", err)
	}
	if _, err := f.WriteString(line); err != nil {
		_ = f.Close()
		return fmt.Errorf("write failures log: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close failures log: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat failures log: %w", err)
	}
	if info.Size() > maxFailuresBytes {
		if err := trimFailures(dir); err != nil {
			return err
		}
	}
	return nil
}

// trimFailures rewrites failures.log so only the newest lines whose
// cumulative size is <= trimTargetBytes are kept. Atomic via
// temp+rename.
func trimFailures(dir string) error {
	path := filepath.Join(dir, failuresFile)
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read failures log: %w", err)
	}
	// Strip a single trailing newline so Split doesn't produce an
	// empty final element.
	trimmed := bytes.TrimRight(b, "\n")
	lines := bytes.Split(trimmed, []byte{'\n'})
	// Walk from newest (end) back; keep lines whose running total
	// (with newlines re-added) stays within target.
	var keep [][]byte
	total := 0
	for i := len(lines) - 1; i >= 0; i-- {
		need := len(lines[i]) + 1 // the trailing newline
		if total+need > trimTargetBytes {
			break
		}
		keep = append([][]byte{lines[i]}, keep...)
		total += need
	}
	// Edge case: a single line already exceeds the target. Keep the
	// newest one anyway — losing the most recent failure is worse
	// than running over the target by one line.
	if len(keep) == 0 && len(lines) > 0 {
		keep = append(keep, lines[len(lines)-1])
	}
	out := bytes.Join(keep, []byte{'\n'})
	out = append(out, '\n')

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0644); err != nil {
		return fmt.Errorf("write failures tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename failures: %w", err)
	}
	return nil
}

// failureLine is the shape AppendFailure's callers write. Only the
// ts field is inspected by this package — the rest is opaque.
type failureLine struct {
	TS string `json:"ts"`
}

// RecentFailures returns the count of lines in failures.log whose
// ts is within `since` of now. A missing file reports 0. A
// malformed line is a real failure — fail loud.
func RecentFailures(dir string, since time.Duration) (int, error) {
	path := filepath.Join(dir, failuresFile)
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("open failures log: %w", err)
	}
	defer f.Close()

	cutoff := time.Now().UTC().Add(-since)
	n := 0
	sc := bufio.NewScanner(f)
	// Snapshot lines are small; a 64 KiB buffer is plenty.
	sc.Buffer(make([]byte, 0, 4096), 1<<16)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var fl failureLine
		if err := json.Unmarshal(line, &fl); err != nil {
			return 0, fmt.Errorf("parse failures log line: %w", err)
		}
		at, err := time.Parse(timestampLayout, fl.TS)
		if err != nil {
			return 0, fmt.Errorf("parse failures log ts %q: %w", fl.TS, err)
		}
		if !at.Before(cutoff) {
			n++
		}
	}
	if err := sc.Err(); err != nil {
		return 0, fmt.Errorf("scan failures log: %w", err)
	}
	return n, nil
}
