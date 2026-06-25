package release

import "github.com/Masterminds/semver/v3"

// IsNewer reports whether latest is strictly newer than current in semver
// order. Both strings may carry a leading "v"; either side that fails to
// parse (empty, "dev", or non-semver) yields false — a missed upgrade
// nudge is preferable to a bogus downgrade nudge.
func IsNewer(latest, current string) bool {
	l, err := semver.NewVersion(latest)
	if err != nil {
		return false
	}
	c, err := semver.NewVersion(current)
	if err != nil {
		return false
	}
	return l.GreaterThan(c)
}
