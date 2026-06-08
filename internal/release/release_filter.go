package release

import (
	"regexp"
	"strings"
)

// stableSemverRE matches v-prefixed semver tags with NO pre-release
// suffix: v1, v1.2, v1.2.3, v01.02.03. Pre-release tags like
// v1.0.0-rc.1 are explicitly excluded by the absence of a "-".
var stableSemverRE = regexp.MustCompile(`^v\d+(\.\d+){0,2}$`)

// preReleaseSemverRE matches v-prefixed semver with a pre-release
// suffix: v1.0.0-rc.1, v2.0.0-beta. Used when includePre is true.
var preReleaseSemverRE = regexp.MustCompile(`^v\d+(\.\d+){0,2}-[A-Za-z0-9.]+$`)

// FilterTags returns the subset of tags that should be considered
// as devm binary releases. Strips:
//   - non v-prefixed tags (e.g. recipes-abc1234, misc-tag)
//   - pre-release tags by default (e.g. v1.0.0-rc.1), unless includePre
//
// Input order is preserved — the caller (go-selfupdate, the picker)
// imposes its own sort. This function is a gate, not a ranker.
func FilterTags(tags []string, includePre bool) ([]string, error) {
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if stableSemverRE.MatchString(t) {
			out = append(out, t)
			continue
		}
		if includePre && preReleaseSemverRE.MatchString(t) {
			out = append(out, t)
		}
	}
	return out, nil
}
