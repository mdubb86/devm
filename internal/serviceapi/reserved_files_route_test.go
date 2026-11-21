package serviceapi

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRoutes_Apply_RejectsFilesReservedName pins the Review Focus #1
// migration signal: a project whose devm.yaml declares the reserved
// files.<project>.<tld> hostname must fail Apply loud, with an error
// naming both the reserved hostname and the fix (remove the entry).
// Anything less silent-mis-routes the user's request to their app
// AWAY from filestash.
func TestRoutes_Apply_RejectsFilesReservedName(t *testing.T) {
	r := NewRoutes("test")
	err := r.Apply("myproj", []Route{
		{Hostname: "files.myproj.test", BackendHost: "127.0.0.1", BackendPort: 8080, Project: "myproj"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "files.myproj.test",
		"error must name the reserved hostname so operator knows what to remove")
	assert.Contains(t, err.Error(), "reserved",
		"error must call out that it's a reserved name")

	// User-owned domains that HAPPEN to start with 'files.' are NOT
	// reserved. Only the exact <project>.<tld> composition is.
	err = r.Apply("myproj", []Route{
		{Hostname: "files.mysite.com", BackendHost: "127.0.0.1", BackendPort: 8080, Project: "myproj"},
	})
	assert.NoError(t, err, "user-owned domains starting with files. must remain accepted")
}

// TestReservedFilestashRoute_Shape pins the (hostname, backend, port)
// triple so a rename or reorder anywhere breaks the test, not a probe.
func TestReservedFilestashRoute_Shape(t *testing.T) {
	r := reservedFilestashRoute("proj", "127.42.0.5", "test")
	assert.Equal(t, "files.proj.test", r.Hostname)
	assert.Equal(t, "127.42.0.5", r.BackendHost)
	assert.Equal(t, filestashServePort, r.BackendPort)
	assert.Equal(t, ModeVM, r.Mode)
}

// TestIsReservedFilesHostname_Exact pins the exact-match contract.
func TestIsReservedFilesHostname_Exact(t *testing.T) {
	assert.True(t, IsReservedFilesHostname("files.p.test", "p", "test"))
	assert.False(t, IsReservedFilesHostname("files.other.test", "p", "test"))
	assert.False(t, IsReservedFilesHostname("files.p.e2e.test", "p", "test"))
	assert.False(t, IsReservedFilesHostname("api.p.test", "p", "test"))
	assert.False(t, IsReservedFilesHostname("filesbogus.p.test", "p", "test"))
	assert.False(t, strings.HasPrefix("files.mysite.com", "files.p."),
		"sanity: user-owned files.mysite.com not touched")
}
