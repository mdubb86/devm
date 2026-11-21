package release

import "testing"

func TestIsNewer(t *testing.T) {
	tests := []struct {
		name    string
		latest  string
		current string
		want    bool
	}{
		// The bug that motivated this helper: cache stored a tag that
		// was latest yesterday; user upgraded; today the !=  check
		// would fire and prompt a downgrade.
		{name: "current strictly newer than latest", latest: "0.3.0", current: "0.3.1", want: false},
		{name: "latest strictly newer than current", latest: "0.3.1", current: "0.3.0", want: true},
		{name: "equal", latest: "0.3.1", current: "0.3.1", want: false},

		{name: "major bump", latest: "1.0.0", current: "0.9.9", want: true},
		{name: "minor bump", latest: "0.4.0", current: "0.3.99", want: true},
		{name: "patch bump", latest: "0.3.2", current: "0.3.1", want: true},

		// "v" prefix tolerance — release tags carry it; ldflag-injected
		// Version doesn't. Both shapes must compare correctly.
		{name: "v-prefix on latest", latest: "v0.3.1", current: "0.3.0", want: true},
		{name: "v-prefix on current", latest: "0.3.1", current: "v0.3.0", want: true},
		{name: "v-prefix on both equal", latest: "v0.3.1", current: "v0.3.1", want: false},

		// Parse failure → false. Wrong-but-silent beats wrong-and-loud:
		// a bogus downgrade nudge is worse than a missed upgrade nudge.
		{name: "empty latest", latest: "", current: "0.3.0", want: false},
		{name: "empty current", latest: "0.3.0", current: "", want: false},
		{name: "garbage latest", latest: "not-a-version", current: "0.3.0", want: false},
		{name: "garbage current", latest: "0.3.0", current: "not-a-version", want: false},
		{name: "dev current", latest: "0.3.0", current: "dev", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsNewer(tc.latest, tc.current)
			if got != tc.want {
				t.Errorf("IsNewer(%q, %q) = %v, want %v", tc.latest, tc.current, got, tc.want)
			}
		})
	}
}
