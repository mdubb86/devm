package serviceapi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// guardTopSampleLimit bounds the fast-path shape check to the first N
// sorted relative paths on each side — enough to catch a mismatched
// tree without hashing every file in a large volume.
const guardTopSampleLimit = 100

// ScanSide is the fast-path summary of one side (Mac or guest) of a
// prospective mutagen sync: total entry count, total byte size, and a
// hash over the first guardTopSampleLimit sorted relative paths. Two
// sides with equal Count, Size, and TopHash are treated as aligned
// without walking every file.
type ScanSide struct {
	Count     int64
	Size      int64
	TopHash   string
	TopSample []string
}

// GuestExec runs script inside the guest and returns its stdout,
// stderr, exit code, and any transport error. It is the injection seam
// between ScanGuest and the real guest transport (tart exec), so tests
// can supply a fake without spinning up a VM.
type GuestExec func(script string) (stdout string, stderr string, exitCode int, err error)

// GuardVerdict is the result of GuardCheck: whether the two sides are
// safe to hand to a fresh mutagen session, the sides that were
// compared, and — on rejection — a human-readable reason.
type GuardVerdict struct {
	OK        bool
	Reason    string
	MacSide   ScanSide
	GuestSide ScanSide
}

// guestScanScriptBody is the guest-side scan logic. It reads its target
// directory from $1, set by the "set --" preamble ScanGuest prepends —
// the guest path is never interpolated directly into the script text,
// so a path containing shell metacharacters can't break out of it.
const guestScanScriptBody = `set -e
cd "$1" 2>/dev/null || { echo count=0 size=0 hash=- top=-; exit 0; }
count=$(find . -mindepth 1 | wc -l | tr -d ' ')
size=$(du -sb . 2>/dev/null | awk '{print $1}')
top=$(find . -mindepth 1 -printf '%P\n' 2>/dev/null | sort | head -100 | shasum -a 256 | awk '{print $1}')
echo "count=$count size=$size hash=$top"
`

// buildGuestScanScript embeds guestPath as a Go-quoted (and therefore
// shell-safe for ordinary paths) literal in a "set --" preamble, then
// appends guestScanScriptBody, which reads that value back out of $1.
func buildGuestScanScript(guestPath string) string {
	return fmt.Sprintf("set -- %q\n%s", guestPath, guestScanScriptBody)
}

// ScanMac walks rootPath on the Mac side and computes its ScanSide: the
// total count of entries below rootPath, their total size in bytes, and
// a sha256 hash over the sorted top guardTopSampleLimit relative paths.
// An empty or missing directory returns the zero ScanSide.
func ScanMac(rootPath string) (ScanSide, error) {
	var count, size int64
	var relPaths []string

	err := filepath.WalkDir(rootPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == rootPath {
			return nil
		}
		rel, relErr := filepath.Rel(rootPath, path)
		if relErr != nil {
			return relErr
		}
		count++
		relPaths = append(relPaths, rel)
		if !d.IsDir() {
			info, infoErr := d.Info()
			if infoErr != nil {
				return infoErr
			}
			size += info.Size()
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ScanSide{}, nil
		}
		return ScanSide{}, fmt.Errorf("mutagen guard: scan mac %s: %w", rootPath, err)
	}

	if count == 0 {
		return ScanSide{}, nil
	}

	sort.Strings(relPaths)
	top := relPaths
	if len(top) > guardTopSampleLimit {
		top = top[:guardTopSampleLimit]
	}

	return ScanSide{
		Count:     count,
		Size:      size,
		TopHash:   hashTopSample(top),
		TopSample: top,
	}, nil
}

// hashTopSample sha256-hashes the newline-joined sample, matching the
// guest script's `sort | head -100 | shasum -a 256` pipeline byte for
// byte (each path followed by a newline).
func hashTopSample(sample []string) string {
	if len(sample) == 0 {
		return ""
	}
	h := sha256.New()
	for _, p := range sample {
		h.Write([]byte(p))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ScanGuest runs the fixed guest-side scan script against guestPath via
// exec and parses its "count=N size=B hash=H" stdout into a ScanSide.
// The script's "hash=-" sentinel (missing or empty target) parses to
// the zero ScanSide.
func ScanGuest(exec GuestExec, guestPath string) (ScanSide, error) {
	stdout, stderr, exitCode, err := exec(buildGuestScanScript(guestPath))
	if err != nil {
		return ScanSide{}, fmt.Errorf("mutagen guard: scan guest %s: %w", guestPath, err)
	}
	if exitCode != 0 {
		return ScanSide{}, fmt.Errorf("mutagen guard: scan guest %s: exit %d: %s", guestPath, exitCode, strings.TrimSpace(stderr))
	}
	return parseGuestScan(stdout)
}

// parseGuestScan parses the guest script's "count=N size=B hash=H"
// stdout line into a ScanSide. "hash=-" parses to an empty TopHash.
func parseGuestScan(stdout string) (ScanSide, error) {
	var side ScanSide
	for _, f := range strings.Fields(stdout) {
		key, val, ok := strings.Cut(f, "=")
		if !ok {
			continue
		}
		switch key {
		case "count":
			n, err := strconv.ParseInt(val, 10, 64)
			if err != nil {
				return ScanSide{}, fmt.Errorf("mutagen guard: parse count %q: %w", val, err)
			}
			side.Count = n
		case "size":
			n, err := strconv.ParseInt(val, 10, 64)
			if err != nil {
				return ScanSide{}, fmt.Errorf("mutagen guard: parse size %q: %w", val, err)
			}
			side.Size = n
		case "hash":
			if val != "-" {
				side.TopHash = val
			}
		}
	}
	return side, nil
}

// GuardCheck implements the in-sync guard's fast-path decision: an
// empty side never conflicts (nothing to reconcile against), and two
// populated sides pass only when Count, Size, and TopHash all agree.
// Any other combination is rejected with a reason describing the
// mismatch.
func GuardCheck(mac, guest ScanSide) GuardVerdict {
	v := GuardVerdict{MacSide: mac, GuestSide: guest}

	if mac.Count == 0 || guest.Count == 0 {
		v.OK = true
		return v
	}

	if mac.Count != guest.Count {
		v.Reason = fmt.Sprintf("mac and guest entry counts differ (mac=%d, guest=%d)", mac.Count, guest.Count)
		return v
	}

	if mac.Size != guest.Size {
		v.Reason = fmt.Sprintf("mac and guest total sizes differ (mac=%d bytes, guest=%d bytes)", mac.Size, guest.Size)
		return v
	}

	if mac.TopHash != guest.TopHash {
		v.Reason = fmt.Sprintf("mac and guest content shape differs (top-%d path hash mismatch)", guardTopSampleLimit)
		return v
	}

	v.OK = true
	return v
}
