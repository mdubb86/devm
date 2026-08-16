package serviceapi

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/supervisor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errStubSpawnFailed is the sentinel error stubSpawnFailingOnCall
// returns from the failing call.
var errStubSpawnFailed = errors.New("stub spawn failure")

// spawnedPackagesCfg records one call to the stubbed spawnIronProxyFn.
type spawnedPackagesCfg struct {
	allowList []string
}

// stubSpawnRecorder installs a spawnIronProxyFn stub that records the
// AllowList of every config it's asked to spawn, and always succeeds.
// Returns the recorded calls slice (grows in place) and a restore func.
func stubSpawnRecorder(t *testing.T) *[]spawnedPackagesCfg {
	t.Helper()
	calls := []spawnedPackagesCfg{}
	orig := spawnIronProxyFn
	t.Cleanup(func() { spawnIronProxyFn = orig })
	spawnIronProxyFn = func(_ context.Context, _ identity.Config, _ *supervisor.Supervisor, _ string, proxyCfg IronProxyConfig, _ *Denials) error {
		calls = append(calls, spawnedPackagesCfg{allowList: append([]string{}, proxyCfg.AllowList...)})
		return nil
	}
	return &calls
}

// stubSpawnFailingOnCall installs a spawnIronProxyFn stub that fails on
// the given 1-indexed call number and succeeds otherwise.
func stubSpawnFailingOnCall(t *testing.T, failOn int) {
	t.Helper()
	orig := spawnIronProxyFn
	t.Cleanup(func() { spawnIronProxyFn = orig })
	n := 0
	spawnIronProxyFn = func(_ context.Context, _ identity.Config, _ *supervisor.Supervisor, _ string, _ IronProxyConfig, _ *Denials) error {
		n++
		if n == failOn {
			return errStubSpawnFailed
		}
		return nil
	}
}

// seedPackagesFixture writes a real iron-proxy ports config on disk
// (rebuildIronProxyConfig reads ports from here) and returns a
// ready-to-use realPackagesApplier plus a snapCfg whose Network.Allow
// mirrors allowList — rebuildIronProxyConfig derives the AllowList from
// snapCfg via docker.EffectiveAllowlist, not from the on-disk file.
func seedPackagesFixture(t *testing.T, projectID string, allowList []string, execScript func(ctx context.Context, vmName, script string) (int, string)) (*realPackagesApplier, schema.Config) {
	t.Helper()
	cfgPath, err := IronProxyConfigPath(identity.Prod, projectID)
	require.NoError(t, err)
	require.NoError(t, writeIronProxyConfig(cfgPath, IronProxyConfig{
		HTTPListen:  "127.0.0.1:9080",
		HTTPSListen: "127.0.0.1:9443",
		DNSListen:   "127.0.0.1:9053",
	}))

	allow := make([]schema.AllowEntry, 0, len(allowList))
	for _, h := range allowList {
		allow = append(allow, schema.AllowEntry{Host: h})
	}
	snapCfg := schema.Config{
		Project: schema.Project{Name: projectID},
		Network: schema.Network{Allow: allow},
	}

	a := &realPackagesApplier{
		cfg:        identity.Prod,
		sup:        supervisor.New(t.TempDir()),
		execScript: execScript,
		healthWait: func(string) bool { return true },
	}
	return a, snapCfg
}

func TestApplyPackages_WidensThenRestores(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const projectID = "pkg-widen"

	var execedScript string
	a, snapCfg := seedPackagesFixture(t, projectID, []string{"api.anthropic.com"},
		func(_ context.Context, vmName, script string) (int, string) {
			assert.Equal(t, projectID, vmName)
			execedScript = script
			return 0, ""
		})
	calls := stubSpawnRecorder(t)

	err := a.ApplyPackages(context.Background(), projectID, snapCfg, []string{"sl"}, nil)
	require.NoError(t, err)

	require.Len(t, *calls, 2, "expected widen spawn + restore spawn")
	assert.Equal(t, []string{"api.anthropic.com", "deb.debian.org", "security.debian.org"}, (*calls)[0].allowList)
	assert.Equal(t, []string{"api.anthropic.com"}, (*calls)[1].allowList)
	assert.Contains(t, execedScript, "install -y 'sl'")
}

func TestApplyPackages_AptFailureStillRestores(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const projectID = "pkg-apt-fail"

	a, snapCfg := seedPackagesFixture(t, projectID, []string{"api.anthropic.com"},
		func(_ context.Context, _ string, _ string) (int, string) {
			return 100, "E: Unable to locate package nope"
		})
	calls := stubSpawnRecorder(t)

	err := a.ApplyPackages(context.Background(), projectID, snapCfg, []string{"nope"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E: Unable to locate package nope")

	require.Len(t, *calls, 2, "restore spawn must still happen despite apt failure")
	assert.Equal(t, []string{"api.anthropic.com"}, (*calls)[1].allowList)
}

func TestApplyPackages_DockerAddsAptRepoHost(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const projectID = "pkg-docker"

	a, snapCfg := seedPackagesFixture(t, projectID, []string{"example.com"},
		func(_ context.Context, _ string, _ string) (int, string) { return 0, "" })
	snapCfg.Docker = true
	calls := stubSpawnRecorder(t)

	err := a.ApplyPackages(context.Background(), projectID, snapCfg, []string{"sl"}, nil)
	require.NoError(t, err)

	require.Len(t, *calls, 2)
	assert.Contains(t, (*calls)[0].allowList, "download.docker.com")
}

func TestApplyPackages_RestoreFailureSurfaces(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const projectID = "pkg-restore-fail"

	a, snapCfg := seedPackagesFixture(t, projectID, []string{"api.anthropic.com"},
		func(_ context.Context, _ string, _ string) (int, string) { return 0, "" })
	stubSpawnFailingOnCall(t, 2)

	err := a.ApplyPackages(context.Background(), projectID, snapCfg, []string{"sl"}, nil)
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "restore")
}
