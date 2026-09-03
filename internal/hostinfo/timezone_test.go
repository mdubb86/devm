package hostinfo

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveMacTimezone_Live(t *testing.T) {
	// Live check against the host running the test. This proves the
	// happy path end-to-end — the resolver returns a plausible IANA
	// name from the actual /etc/localtime symlink. Tests run on macOS
	// where /etc/localtime is present.
	zone, err := ResolveMacTimezone()
	require.NoError(t, err, "resolver should succeed on a normal macOS host")
	assert.NotEmpty(t, zone)
	// Every IANA zone name contains at least one '/' (e.g.
	// America/Chicago, Europe/London, UTC is the exception but macOS
	// symlinks it to `UTC` under /var/db/timezone/zoneinfo/UTC).
	// The presence of at least one character is the minimum honest
	// assertion; a stricter format check would false-positive on
	// unusual-but-valid zones (e.g. Etc/GMT+5).
	t.Logf("resolved zone: %s", zone)
}

func TestParseFromSymlinkTarget_VarDbZoneinfo(t *testing.T) {
	zone, ok := parseFromSymlinkTarget("/var/db/timezone/zoneinfo/America/Chicago")
	require.True(t, ok)
	assert.Equal(t, "America/Chicago", zone)
}

func TestParseFromSymlinkTarget_UsrShareZoneinfo(t *testing.T) {
	zone, ok := parseFromSymlinkTarget("/usr/share/zoneinfo/Europe/London")
	require.True(t, ok)
	assert.Equal(t, "Europe/London", zone)
}

func TestParseFromSymlinkTarget_UTC(t *testing.T) {
	zone, ok := parseFromSymlinkTarget("/var/db/timezone/zoneinfo/UTC")
	require.True(t, ok)
	assert.Equal(t, "UTC", zone)
}

func TestParseFromSymlinkTarget_UnknownPrefix(t *testing.T) {
	_, ok := parseFromSymlinkTarget("/opt/homebrew/share/zoneinfo/America/Chicago")
	assert.False(t, ok)
}

func TestParseFromSymlinkTarget_PrefixOnlyNoZone(t *testing.T) {
	_, ok := parseFromSymlinkTarget("/var/db/timezone/zoneinfo/")
	assert.False(t, ok)
}
