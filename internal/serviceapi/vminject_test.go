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
