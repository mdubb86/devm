package serviceapi

import (
	"context"
	"strings"
	"testing"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyPackages_ExecsUnderCurrentPolicy(t *testing.T) {
	const projectID = "pkg-exec"

	var gotVMName, gotScript string
	a := &realPackagesApplier{
		execScript: func(_ context.Context, vmName, script string) (int, string) {
			gotVMName = vmName
			gotScript = script
			return 0, ""
		},
	}

	err := a.ApplyPackages(context.Background(), projectID, schema.Config{}, "", []string{"sl"}, nil)
	require.NoError(t, err)

	assert.Equal(t, projectID, gotVMName)
	assert.Contains(t, gotScript, "install -y 'sl'")

	// Regression: the shipped script must be self-contained. The
	// converge body calls the `apt_run` bash function, which is defined
	// by render.AptRetryHelper. Live reconcile pipes the body straight
	// to bash with no umbrella script, so the helper has to be
	// prepended here — the v0.22.1 apt_run introduction shipped without
	// it and every live packages: diff exited 127 (command not found).
	assert.Contains(t, gotScript, "apt_run() {",
		"shipped script must define apt_run before calling it")
	defIdx := strings.Index(gotScript, "apt_run() {")
	callIdx := strings.Index(gotScript, "apt_run install")
	assert.True(t, defIdx >= 0 && callIdx > defIdx,
		"apt_run must be defined before it is called:\n%s", gotScript)
}

func TestApplyPackages_FailurePropagates(t *testing.T) {
	const projectID = "pkg-fail"

	a := &realPackagesApplier{
		execScript: func(_ context.Context, _, _ string) (int, string) {
			return 100, "E: Unable to locate package nope"
		},
	}

	err := a.ApplyPackages(context.Background(), projectID, schema.Config{}, "", []string{"nope"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "apt exit 100")
	assert.Contains(t, err.Error(), "E: Unable to locate package nope")
	assert.NotContains(t, err.Error(), "network.allow", "generic apt failures must not carry the egress hint")
}

// TestApplyPackages_FailureWithEgressSignature_CarriesHint covers a
// converge failure whose stderr carries the 403 signature devm's
// PolicyAuthority hands back verbatim when a mirror is blocked by
// network.allow — the error must carry the fix hint.
func TestApplyPackages_FailureWithEgressSignature_CarriesHint(t *testing.T) {
	const projectID = "pkg-blocked"

	a := &realPackagesApplier{
		execScript: func(_ context.Context, _, _ string) (int, string) {
			return 100, "E: Failed to fetch http://deb.debian.org/debian/dists/stable/InRelease  403  Forbidden"
		},
	}

	err := a.ApplyPackages(context.Background(), projectID, schema.Config{}, "", []string{"sl"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deb.debian.org")
	assert.Contains(t, err.Error(), "network.allow — add deb.debian.org and security.debian.org")
	assert.Contains(t, err.Error(), "devm passthrough")
	assert.Contains(t, err.Error(), "devm denials")
}

func TestApplyPackages_EmptyNoop(t *testing.T) {
	const projectID = "pkg-noop"

	called := false
	a := &realPackagesApplier{
		execScript: func(_ context.Context, _, _ string) (int, string) {
			called = true
			return 0, ""
		},
	}

	err := a.ApplyPackages(context.Background(), projectID, schema.Config{}, "", nil, nil)
	require.NoError(t, err)
	assert.False(t, called, "no adds/removes must not exec anything")
}
