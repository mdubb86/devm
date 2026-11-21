package schema

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// SizeOrPercent is a sizing value that is either an absolute byte
// count or a fraction of some base dimension chosen by the caller.
// Exactly one of Absolute or Percent is non-zero after a successful
// ParseSizeOrPercent; the other stays at its zero value.
type SizeOrPercent struct {
	// Absolute is a positive byte count when the input named a
	// concrete size (e.g. "6G" → 6 * 1024^3).
	Absolute int64
	// Percent is a positive ratio when the input named a percentage
	// (e.g. "15%" → 0.15). Not bounded above; "200%" → 2.0.
	Percent float64
}

// Resolve returns the concrete byte count this value represents
// against the supplied base. An absolute value ignores base; a
// percent value multiplies the base and truncates to whole bytes.
func (s SizeOrPercent) Resolve(base int64) int64 {
	if s.Absolute != 0 {
		return s.Absolute
	}
	return int64(float64(base) * s.Percent)
}

// ParseSizeOrPercent parses either an absolute size ("6G", "6GB",
// "6GiB", case-insensitive — binary gibibytes, matching ParseMemorySize
// / ParseDiskSize) or a percentage ("15%"). The magnitude must be
// strictly positive; zero, negative, and malformed inputs return an
// error that names the offending input.
func ParseSizeOrPercent(s string) (SizeOrPercent, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return SizeOrPercent{}, fmt.Errorf("sizeOrPercent: value is empty")
	}
	if strings.HasSuffix(raw, "%") {
		num := strings.TrimSpace(raw[:len(raw)-1])
		if num == "" {
			return SizeOrPercent{}, fmt.Errorf("sizeOrPercent: %q is missing a number before %%", s)
		}
		pct, err := strconv.ParseFloat(num, 64)
		if err != nil {
			return SizeOrPercent{}, fmt.Errorf("sizeOrPercent: %q is not a valid percentage", s)
		}
		if math.IsNaN(pct) || math.IsInf(pct, 0) {
			return SizeOrPercent{}, fmt.Errorf("sizeOrPercent: %q must be a finite percentage", s)
		}
		if pct <= 0 {
			return SizeOrPercent{}, fmt.Errorf("sizeOrPercent: %q must be a positive percentage", s)
		}
		return SizeOrPercent{Percent: pct / 100.0}, nil
	}
	gigs, err := parseGibibytes(raw)
	if err != nil {
		return SizeOrPercent{}, fmt.Errorf("sizeOrPercent: %q: %w", s, err)
	}
	return SizeOrPercent{Absolute: int64(gigs) * 1024 * 1024 * 1024}, nil
}

// parseGibibytes reads a positive integer followed by a G / GB / GiB
// suffix (case-insensitive) and returns the integer magnitude. Shared
// grammar for every size field (`disk`, `memory`, `swap`,
// `docker.buildCache`) — callers wrap the terse error with their own
// field prefix.
func parseGibibytes(s string) (int, error) {
	raw := strings.TrimSpace(s)
	upper := strings.ToUpper(raw)
	var numStr string
	switch {
	case strings.HasSuffix(upper, "GIB"):
		numStr = raw[:len(raw)-3]
	case strings.HasSuffix(upper, "GB"):
		numStr = raw[:len(raw)-2]
	case strings.HasSuffix(upper, "G"):
		numStr = raw[:len(raw)-1]
	default:
		return 0, fmt.Errorf("must use a gigabyte suffix, e.g. \"6G\"")
	}
	n, err := strconv.Atoi(strings.TrimSpace(numStr))
	if err != nil {
		return 0, fmt.Errorf("magnitude is not an integer")
	}
	if n <= 0 {
		return 0, fmt.Errorf("magnitude must be positive")
	}
	return n, nil
}
