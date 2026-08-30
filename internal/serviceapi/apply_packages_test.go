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
	"gopkg.in/yaml.v3"
)

// errStubSpawnFailed is the sentinel error the spawn stub returns from
// its designated failing call.
var errStubSpawnFailed = errors.New("stub spawn failure")

// packagesEvent is one recorded step of a realPackagesApplier call,
// captured in call order across both spawnIronProxyFn and execScript so
// ordering can be asserted directly (widen spawn -> exec -> restore
// spawn) instead of inferred from two independently-ordered slices.
//
// kind is "spawn:widened" for the first spawn call ApplyPackages ever
// makes, "spawn:orig" for every subsequent one (ApplyPackages only ever
// respawns the widened config once, up front — every later respawn,
// whether the post-exec restore or a best-effort restore after a widen
// failure, is always back to orig), or "exec" for the guest script run.
type packagesEvent struct {
	kind      string
	allowList []string        // populated for "spawn:*" events
	proxyCfg  IronProxyConfig // populated for "spawn:*" events
	script    string          // populated for "exec" events
}

// newPackagesEventRecorder installs a spawnIronProxyFn stub and returns
// an execScript stub, both appending to one shared, call-ordered event
// log. spawnFailOn, if > 0, is the 1-indexed spawn call that returns
// errStubSpawnFailed instead of succeeding (the call is still recorded
// — spawnIronProxyFn was invoked either way). execResult supplies the
// (exitCode, stderr) the exec stub returns.
func newPackagesEventRecorder(t *testing.T, projectID string, spawnFailOn int, execResult func() (int, string)) (*[]packagesEvent, func(ctx context.Context, vmName, script string) (int, string)) {
	t.Helper()
	events := []packagesEvent{}
	origSpawn := spawnIronProxyFn
	t.Cleanup(func() { spawnIronProxyFn = origSpawn })

	n := 0
	spawnIronProxyFn = func(_ context.Context, _ identity.Config, _ *supervisor.Supervisor, _ string, proxyCfg IronProxyConfig) error {
		n++
		kind := "spawn:orig"
		if n == 1 {
			kind = "spawn:widened"
		}
		events = append(events, packagesEvent{
			kind:      kind,
			allowList: append([]string{}, proxyCfg.AllowList...),
			proxyCfg:  proxyCfg,
		})
		if n == spawnFailOn {
			return errStubSpawnFailed
		}
		return nil
	}

	execScript := func(_ context.Context, vmName, script string) (int, string) {
		assert.Equal(t, projectID, vmName)
		events = append(events, packagesEvent{kind: "exec", script: script})
		return execResult()
	}
	return &events, execScript
}

// eventKinds extracts the kind of each event, for a compact ordering
// assertion.
func eventKinds(events []packagesEvent) []string {
	kinds := make([]string, len(events))
	for i, e := range events {
		kinds[i] = e.kind
	}
	return kinds
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
		HTTPListen:   "127.0.0.1:9080",
		HTTPSListen:  "127.0.0.1:9443",
		TunnelListen: "127.0.0.1:9081",
		DNSListen:    "127.0.0.1:9053",
		PolicyTarget: "unix:///tmp/p.sock",
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

	events, execScript := newPackagesEventRecorder(t, projectID, 0, func() (int, string) { return 0, "" })
	a, snapCfg := seedPackagesFixture(t, projectID, []string{"api.anthropic.com"}, execScript)

	err := a.ApplyPackages(context.Background(), projectID, snapCfg, "", []string{"sl"}, nil)
	require.NoError(t, err)

	require.Len(t, *events, 3, "expected widen spawn, exec, restore spawn in order")
	assert.Equal(t, []string{"spawn:widened", "exec", "spawn:orig"}, eventKinds(*events))
	assert.Equal(t, []string{"api.anthropic.com", "deb.debian.org", "security.debian.org"}, (*events)[0].allowList)
	assert.Contains(t, (*events)[1].script, "install -y 'sl'")
	assert.Equal(t, []string{"api.anthropic.com"}, (*events)[2].allowList)
}

func TestApplyPackages_AptFailureStillRestores(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const projectID = "pkg-apt-fail"

	events, execScript := newPackagesEventRecorder(t, projectID, 0, func() (int, string) {
		return 100, "E: Unable to locate package nope"
	})
	a, snapCfg := seedPackagesFixture(t, projectID, []string{"api.anthropic.com"}, execScript)

	err := a.ApplyPackages(context.Background(), projectID, snapCfg, "", []string{"nope"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E: Unable to locate package nope")

	assert.Equal(t, []string{"spawn:widened", "exec", "spawn:orig"}, eventKinds(*events), "restore spawn must still happen despite apt failure")
	assert.Equal(t, []string{"api.anthropic.com"}, (*events)[2].allowList)
}

func TestApplyPackages_DockerAddsAptRepoHost(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const projectID = "pkg-docker"

	events, execScript := newPackagesEventRecorder(t, projectID, 0, func() (int, string) { return 0, "" })
	a, snapCfg := seedPackagesFixture(t, projectID, []string{"example.com"}, execScript)
	snapCfg.Docker = true

	err := a.ApplyPackages(context.Background(), projectID, snapCfg, "", []string{"sl"}, nil)
	require.NoError(t, err)

	assert.Equal(t, []string{"spawn:widened", "exec", "spawn:orig"}, eventKinds(*events))
	assert.Contains(t, (*events)[0].allowList, "download.docker.com")
}

func TestApplyPackages_RestoreFailureSurfaces(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const projectID = "pkg-restore-fail"

	// spawn call #2 is the post-exec restore — fail it so a successful
	// apt run still surfaces an error because the restore itself failed.
	events, execScript := newPackagesEventRecorder(t, projectID, 2, func() (int, string) { return 0, "" })
	a, snapCfg := seedPackagesFixture(t, projectID, []string{"api.anthropic.com"}, execScript)

	err := a.ApplyPackages(context.Background(), projectID, snapCfg, "", []string{"sl"}, nil)
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "restore")

	assert.Equal(t, []string{"spawn:widened", "exec", "spawn:orig"}, eventKinds(*events), "restore spawn must still be attempted even though it fails")
}

// TestApplyPackages_WidenHealthTimeout_BestEffortRestores covers the
// case where the widen respawn's spawnIronProxyFn call succeeds (the
// widened config is live) but the subsequent health-wait times out —
// ApplyPackages must still attempt a best-effort restore before
// returning the widen error, since the widened allowlist is already on
// disk and running. No exec call must happen (apt never runs), and a
// failure of THIS restore attempt is only logged, not returned.
func TestApplyPackages_WidenHealthTimeout_BestEffortRestores(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const projectID = "pkg-widen-timeout"

	events, execScript := newPackagesEventRecorder(t, projectID, 0, func() (int, string) { return 0, "" })
	a, snapCfg := seedPackagesFixture(t, projectID, []string{"api.anthropic.com"}, execScript)

	healthCalls := 0
	a.healthWait = func(string) bool {
		healthCalls++
		return healthCalls != 1 // first (widen) health-wait fails; restore's succeeds
	}

	err := a.ApplyPackages(context.Background(), projectID, snapCfg, "", []string{"sl"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "widen egress")

	// Widen spawn happened, then a best-effort restore spawn — but no
	// exec, since ApplyPackages returns before ever running the script.
	assert.Equal(t, []string{"spawn:widened", "spawn:orig"}, eventKinds(*events))
	assert.Equal(t, []string{"api.anthropic.com"}, (*events)[1].allowList, "best-effort restore must target the original allowlist")
}

// TestApplyPackages_NoProjectAllowlist_RestoreCloses covers a project
// whose effective allowlist is empty (no network.allow at all). The
// restore spawn's config must still carry an allowlist transform (empty
// domains = deny-all), because iron-proxy applies no egress check
// whatsoever when the transform is absent: an omitted transform is
// allow-all, not deny-all, so a restore that merely drops the apt hosts
// from the config would leave deb.debian.org reachable.
func TestApplyPackages_NoProjectAllowlist_RestoreCloses(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const projectID = "pkg-no-allowlist"

	events, execScript := newPackagesEventRecorder(t, projectID, 0, func() (int, string) { return 0, "" })
	a, snapCfg := seedPackagesFixture(t, projectID, nil, execScript)

	err := a.ApplyPackages(context.Background(), projectID, snapCfg, "", []string{"sl"}, nil)
	require.NoError(t, err)

	require.Equal(t, []string{"spawn:widened", "exec", "spawn:orig"}, eventKinds(*events))
	assert.Equal(t, []string{"deb.debian.org", "security.debian.org"}, (*events)[0].allowList)
	assert.Empty(t, (*events)[2].allowList)

	// The window is only closed if the config the restore spawns with
	// actually enforces something. rebuildIronProxyConfig fills
	// PolicyTarget unconditionally, and YAML() refuses to render
	// without one — so the emitted grpc transform's target proves the
	// restore delegates to the daemon's PolicyAuthority (re-Set to the
	// original, now-empty allowlist) rather than iron-proxy running
	// allow-all. Assert on the emitted YAML, not just the AllowList
	// slice — that's the layer where the window stayed open.
	blob, err := (*events)[2].proxyCfg.YAML()
	require.NoError(t, err)
	var restored map[string]any
	require.NoError(t, yaml.Unmarshal(blob, &restored))

	transforms, ok := restored["transforms"].([]any)
	require.True(t, ok, "restore config must carry a transforms list; without one iron-proxy allows every host")
	require.Len(t, transforms, 1)
	transform := transforms[0].(map[string]any)
	assert.Equal(t, "grpc", transform["name"])
	assert.NotEmpty(t, transform["config"].(map[string]any)["target"],
		"grpc transform must carry a non-empty PolicyTarget or iron-proxy allows every host")
}

func TestApplyPackages_EmptyNoop(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const projectID = "pkg-noop"

	events, execScript := newPackagesEventRecorder(t, projectID, 0, func() (int, string) { return 0, "" })
	a, snapCfg := seedPackagesFixture(t, projectID, []string{"api.anthropic.com"}, execScript)

	err := a.ApplyPackages(context.Background(), projectID, snapCfg, "", nil, nil)
	require.NoError(t, err)
	assert.Empty(t, *events, "no adds/removes must not spawn iron-proxy or exec anything")
}
