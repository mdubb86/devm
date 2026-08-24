package serviceapi

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildGrowRootScript(t *testing.T) {
	s := buildGrowRootScript()
	assert.Contains(t, s, "growpart /dev/vda 1")
	assert.Contains(t, s, "resize2fs /dev/vda1")
	// PATH must include /sbin so growpart finds sfdisk and resize2fs.
	assert.Contains(t, s, "/sbin")
	// growpart's no-op exit must be tolerated.
	assert.True(t, strings.Contains(s, "growpart /dev/vda 1 || true"))
}

func TestParseExtraMounts_RWAndRO(t *testing.T) {
	got := parseExtraMounts([]string{
		"/Users/x/data",
		"/Users/x/ro-thing:ro",
		"", // dropped
	})
	require.Len(t, got, 2)
	assert.Equal(t, extraMount{hostPath: "/Users/x/data", readOnly: false}, got[0])
	assert.Equal(t, extraMount{hostPath: "/Users/x/ro-thing", readOnly: true}, got[1])
}

func TestBuildExtraMountScript_RW(t *testing.T) {
	script := buildExtraMountScript("extra_0", "/Users/x/data", false)
	assert.Contains(t, script, "mkdir -p /Users/x/data")
	assert.Contains(t, script, "mount -t virtiofs extra_0 /Users/x/data")
	assert.Contains(t, script, "extra_0 /Users/x/data virtiofs rw,_netdev 0 0")
	// RW must not pass -o ro to mount.
	assert.NotContains(t, script, "-o ro")
}

func TestBuildExtraMountScript_ReadOnly(t *testing.T) {
	script := buildExtraMountScript("extra_1", "/Users/x/ro-thing", true)
	assert.Contains(t, script, "mount -o ro -t virtiofs extra_1 /Users/x/ro-thing")
	assert.Contains(t, script, "extra_1 /Users/x/ro-thing virtiofs ro,_netdev 0 0")
}

// TestVminject_NoDirectEtcEnvironmentWrite pins that vminject.go
// contains no `tee /etc/environment` — /etc/environment travels
// with the bundle now (see internal/devmbundle + install.sh). A
// future refactor that reintroduces a direct write would create
// a race with the bundle install and split the source of truth
// for env transport.
func TestVminject_NoDirectEtcEnvironmentWrite(t *testing.T) {
	src, err := os.ReadFile("vminject.go")
	require.NoError(t, err)
	assert.NotContains(t, string(src), "tee /etc/environment",
		"vminject.go must not shell out to `tee /etc/environment` — use bundle path")
	assert.NotContains(t, string(src), "> /etc/environment",
		"vminject.go must not redirect to /etc/environment — use bundle path")
}
