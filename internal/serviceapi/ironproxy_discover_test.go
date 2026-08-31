package serviceapi

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mdubb86/devm/internal/identity"
	transformv1 "github.com/mdubb86/devm/internal/ironproxy/transformv1"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/supervisor"
)

func TestParseIronProxyProjectID(t *testing.T) {
	cases := []struct {
		name          string
		command       string
		wantProjectID string
		wantOK        bool
	}{
		{
			name:          "canonical path with space in Application Support",
			command:       "/Users/michael/workspace/devm/bin/iron-proxy -config /Users/michael/Library/Application Support/devm/iron-proxy/myproj.yaml",
			wantProjectID: "myproj",
			wantOK:        true,
		},
		{
			name:          "project id with hyphens and dots",
			command:       "/opt/iron-proxy -config /tmp/iron-proxy/foo-bar.baz.yaml",
			wantProjectID: "foo-bar.baz",
			wantOK:        true,
		},
		{
			name:    "no /iron-proxy/ in argv",
			command: "/bin/sh -c true",
			wantOK:  false,
		},
		{
			name:    "no .yaml suffix",
			command: "/bin/iron-proxy -config /tmp/iron-proxy/myproj.json",
			wantOK:  false,
		},
		{
			name:    "empty project id",
			command: "/bin/iron-proxy -config /tmp/iron-proxy/.yaml",
			wantOK:  false,
		},
		{
			name:    "binary path component only (no -config arg)",
			command: "/path/iron-proxy --version",
			wantOK:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			projectID, ok := parseIronProxyProjectID(tc.command)
			assert.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				assert.Equal(t, tc.wantProjectID, projectID)
			}
		})
	}
}

func TestParseIronProxyProcesses(t *testing.T) {
	binary := "/Users/michael/workspace/devm/bin/iron-proxy"
	psOutput := `  100 /usr/sbin/syslogd
  200 /Users/michael/workspace/devm/bin/iron-proxy -config /Users/michael/Library/Application Support/devm/iron-proxy/projA.yaml
  300 /Users/michael/workspace/devm/bin/iron-proxy -config /Users/michael/Library/Application Support/devm/iron-proxy/projB.yaml
  400 /opt/homebrew/bin/iron-proxy -config /tmp/iron-proxy/strangerprojC.yaml
  500 /Users/michael/workspace/devm/bin/iron-proxy --help
notanint /Users/michael/workspace/devm/bin/iron-proxy -config /tmp/iron-proxy/bad.yaml
`
	got := parseIronProxyProcesses(psOutput, binary)
	byID := map[string]DiscoveredIronProxy{}
	for _, p := range got {
		byID[p.ProjectID] = p
	}

	assert.Equal(t, 200, byID["projA"].PID)
	assert.Equal(t, 300, byID["projB"].PID)
	assert.NotContains(t, byID, "strangerprojC", "wrong binary path must not be adopted")
	assert.NotContains(t, byID, "bad", "malformed pid line must be skipped")
	assert.Len(t, got, 2)
}

func TestParseIronProxyProcesses_EmptyInput(t *testing.T) {
	got := parseIronProxyProcesses("", "/anywhere/iron-proxy")
	assert.Empty(t, got)
}

func TestLoadIronProxyInfoFromConfig(t *testing.T) {
	// Round-trip: write an IronProxyConfig via YAML(), then read it back
	// via loadIronProxyInfoFromConfig. Pins that the reader stays
	// in sync with the writer — if either shifts, rehydration silently
	// starts returning zero values and the daemon rebuilds the softnet
	// enforced-policy endpoint with the wrong ports after a restart.
	cfg := IronProxyConfig{
		HTTPListen:   "192.168.64.1:59481",
		HTTPSListen:  "192.168.64.1:59482",
		TunnelListen: "192.168.64.1:59484",
		DNSListen:    "192.168.64.1:59483",
		DNSProxyIP:   "192.0.2.1",
		CACertPath:   "/tmp/ca.crt",
		CAKeyPath:    "/tmp/ca.key",
		PolicyTarget: "unix:///tmp/p.sock",
	}
	blob, err := cfg.YAML()
	require.NoError(t, err)

	dir := t.TempDir()
	path := filepath.Join(dir, "proj.yaml")
	require.NoError(t, os.WriteFile(path, blob, 0600))

	info, err := loadIronProxyInfoFromConfig(path)
	require.NoError(t, err)
	assert.Equal(t, projectInfo{
		PolicySocket: "/tmp/p.sock",
		HTTPPort:     59481,
		HTTPSPort:    59482,
		TunnelPort:   59484,
		DNSPort:      59483,
	}, info)
}

func TestLoadIronProxyInfoFromConfig_MissingFile(t *testing.T) {
	_, err := loadIronProxyInfoFromConfig("/nonexistent/nowhere.yaml")
	assert.Error(t, err)
}

// TestRecoverProjectState_ReplaysSnapshotRoutes covers the
// daemon-restart adoption path (AdoptIronProxies calls this per
// recovered project, after already seeding ironProxyState from the
// project's on-disk iron-proxy config): given a state snapshot whose
// Routes carries the last-applied set — direct, ExposeHost, and
// default proxied routes with substituted BackendHost — recoverProjectState
// replays them verbatim so a daemon restart is invisible to callers.
func TestRecoverProjectState_ReplaysSnapshotRoutes(t *testing.T) {
	const projectID = "recover-proj"
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(func() {
		ironProxyState.del(projectID)
		policyAuthority.PurgeProject(projectID)
	})

	// Mirrors AdoptIronProxies having already rehydrated ironProxyState
	// from the project's on-disk iron-proxy config before calling
	// recoverProjectState.
	ironProxyState.put(projectID, projectInfo{HTTPPort: 59481, HTTPSPort: 59482, DNSPort: 59483})

	require.NoError(t, WriteStateSnapshot(identity.Prod, projectID, StateSnapshot{
		Cfg:       schema.Config{Project: schema.Project{Name: projectID}},
		ProjectIP: "127.42.0.9",
		Routes: []Route{
			{Hostname: "db.recover-proj.test", BackendPort: 5432, Mode: ModeVM, Direct: true, Project: projectID},
			{Hostname: "api.recover-proj.test", BackendHost: "127.42.0.9", BackendPort: 8080, Mode: ModeVM, ExposeHost: true, Project: projectID},
			{Hostname: "web.recover-proj.test", BackendHost: "127.42.0.9", BackendPort: 3000, Mode: ModeVM, Project: projectID},
		},
	}))

	routes := NewRoutes()
	recoverProjectState(context.Background(), identity.Prod, tart.New(), routes, projectID)

	info, ok := ironProxyState.get(projectID)
	assert.True(t, ok)
	assert.Equal(t, 59481, info.HTTPPort, "pre-seeded ports must survive recoverProjectState")

	direct, ok := findRoute(routes, "db.recover-proj.test")
	require.True(t, ok, "direct route must be replayed")
	assert.True(t, direct.Direct, "the direct flag must survive the snapshot round-trip")
	assert.Equal(t, 5432, direct.BackendPort)

	lan, ok := routes.LANLookup("api.recover-proj.test")
	require.True(t, ok, "expose_host route must be replayed into the LAN opt-in map")
	assert.Equal(t, "127.42.0.9", lan.BackendHost, "recovered BackendHost must round-trip through the snapshot verbatim")
	assert.Equal(t, 8080, lan.BackendPort)
	assert.Equal(t, 1, routes.CountLANRoutes())

	web, ok := routes.Lookup("web.recover-proj.test", projectID)
	require.True(t, ok, "default proxied route (the class the pre-mirror recovery deliberately skipped) must now be replayed")
	assert.Equal(t, "127.42.0.9", web.BackendHost)
	assert.Equal(t, 3000, web.BackendPort)
}

// After a daemon restart, recoverProjectState must re-serve the adopted
// project's policy socket with the allowlist recomputed from the state
// snapshot — until it runs, every guest request fail-closes with a 502.
func TestRecoverProjectState_ServesSnapshotAllowlist(t *testing.T) {
	const projectID = "recover-policy-proj"
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(func() {
		ironProxyState.del(projectID)
		policyAuthority.PurgeProject(projectID)
	})

	require.NoError(t, WriteStateSnapshot(identity.Prod, projectID, StateSnapshot{
		Cfg: schema.Config{
			Network: schema.Network{Allow: []schema.AllowEntry{{Host: "example.com"}}},
		},
	}))

	recoverProjectState(context.Background(), identity.Prod, nil, NewRoutes(), projectID)

	sockPath, err := IronPolicySocketPath(identity.Prod, projectID)
	require.NoError(t, err)
	client := dialPolicy(t, sockPath)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := client.TransformRequest(ctx, policyReq("example.com", "GET", "/"))
	require.NoError(t, err)
	require.Equal(t, transformv1.TransformAction_TRANSFORM_ACTION_CONTINUE, resp.GetAction())
	resp, err = client.TransformRequest(ctx, policyReq("blocked.example", "GET", "/"))
	require.NoError(t, err)
	require.Equal(t, transformv1.TransformAction_TRANSFORM_ACTION_REJECT, resp.GetAction())
}

// TestAdoptOneIronProxy_UnreadableConfig_StillServesPolicy covers the
// adoption gap where a discovered iron-proxy's on-disk config is
// missing (or otherwise unreadable) — historically this `continue`d
// past recoverProjectState entirely, leaving the running adopted VM's
// egress fail-closed at 502 forever, since the policy socket was never
// re-served. loadIronProxyInfoFromConfig failing must not stop the
// policy re-serve from happening: ports go unrecovered, but the socket
// still comes up with the allowlist recomputed from the state
// snapshot.
// An adopted iron-proxy whose project has no tart VM at all — not even a
// stopped one — serves nothing and never will: /vm/start spawns a fresh
// proxy, and nothing else dials this one. Adoption must stop it and
// leave no daemon state behind, instead of preserving it forever (the
// accumulation that produced 29 strays and a squatted pool IP in the
// field). A real process stands in for the proxy so the stop path's
// signal delivery is exercised, not stubbed.
func TestAdoptOneIronProxy_StopsProxyWithNoVM(t *testing.T) {
	const projectID = "gc-no-vm-proj"
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(func() {
		ironProxyState.del(projectID)
		policyAuthority.PurgeProject(projectID)
	})

	stand := exec.Command("sleep", "300")
	require.NoError(t, stand.Start())
	t.Cleanup(func() { _ = stand.Process.Kill() })
	// Reap immediately on death: the test is the stand-in's parent, so
	// without a waiter the killed child would linger as a zombie that
	// still answers signal 0 — fooling both this test's liveness check
	// and the supervisor's own death poll. Production adopted pids are
	// never the daemon's children, so no zombie exists there.
	waitCh := make(chan error, 1)
	go func() { waitCh <- stand.Wait() }()

	sup := supervisor.New(t.TempDir())
	vmNames := map[string]bool{"some-other-project": true}
	adoptOneIronProxy(context.Background(), identity.Prod, sup, tart.New(), NewRoutes(), DiscoveredIronProxy{PID: stand.Process.Pid, ProjectID: projectID}, vmNames)

	select {
	case err := <-waitCh:
		var exitErr *exec.ExitError
		require.ErrorAs(t, err, &exitErr, "stand-in must have been signalled, not exited cleanly")
		require.Equal(t, syscall.SIGTERM, exitErr.Sys().(syscall.WaitStatus).Signal(),
			"orphan stop must deliver graceful TERM first")
	case <-time.After(15 * time.Second):
		t.Fatal("orphaned proxy process was not stopped")
	}

	// No state, no policy socket.
	_, ok := ironProxyState.get(projectID)
	assert.False(t, ok, "orphaned project must not enter ironProxyState")
	sockPath, err := IronPolicySocketPath(identity.Prod, projectID)
	require.NoError(t, err)
	_, statErr := os.Stat(sockPath)
	assert.True(t, os.IsNotExist(statErr), "no policy socket may be served for an orphaned project")
}

// Adoption serves the policy socket at the path RECORDED in iron-proxy's
// config (the grpc transform's target — the path the running proxy
// actually dials), never a re-derived one. Re-derivation can disagree
// with the recording when os.TempDir()'s TMPDIR context differs between
// the spawning and adopting daemon runs; the config file is the one
// source of truth for where the proxy dials.
func TestAdoptOneIronProxy_ServesRecordedPolicyTarget(t *testing.T) {
	const projectID = "adopt-recorded-target-proj"
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(func() {
		ironProxyState.del(projectID)
		policyAuthority.PurgeProject(projectID)
	})

	// A recorded target the deriver would never produce.
	recDir, err := os.MkdirTemp("", "polrec")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(recDir) })
	recPath := filepath.Join(recDir, "rec.sock")

	cfgPath, err := IronProxyConfigPath(identity.Prod, projectID)
	require.NoError(t, err)
	require.NoError(t, writeIronProxyConfig(cfgPath, IronProxyConfig{
		HTTPListen:   "127.0.0.1:18080",
		HTTPSListen:  "127.0.0.1:18443",
		TunnelListen: "127.0.0.1:18081",
		DNSListen:    "127.0.0.1:18053",
		DNSProxyIP:   "192.0.2.1",
		CACertPath:   "/tmp/ca.crt",
		CAKeyPath:    "/tmp/ca.key",
		PolicyTarget: "unix://" + recPath,
	}))
	require.NoError(t, WriteStateSnapshot(identity.Prod, projectID, StateSnapshot{
		Cfg: schema.Config{
			Network: schema.Network{Allow: []schema.AllowEntry{{Host: "example.com"}}},
		},
	}))

	sup := supervisor.New(t.TempDir())
	adoptOneIronProxy(context.Background(), identity.Prod, sup, tart.New(), NewRoutes(), DiscoveredIronProxy{PID: 424243, ProjectID: projectID}, map[string]bool{projectID: true})

	client := dialPolicy(t, recPath)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := client.TransformRequest(ctx, policyReq("example.com", "GET", "/"))
	require.NoError(t, err, "policy must be served at the recorded target, where the proxy dials")
	require.Equal(t, transformv1.TransformAction_TRANSFORM_ACTION_CONTINUE, resp.GetAction())

	// And nothing serves at the re-derived location — the recorded path
	// is authoritative, not merely an alias.
	derived, err := IronPolicySocketPath(identity.Prod, projectID)
	require.NoError(t, err)
	_, statErr := os.Stat(derived)
	assert.True(t, os.IsNotExist(statErr),
		"no socket may exist at the re-derived path %s when the config records %s", derived, recPath)
}

func TestAdoptOneIronProxy_UnreadableConfig_StillServesPolicy(t *testing.T) {
	const projectID = "adopt-no-config-proj"
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(func() {
		ironProxyState.del(projectID)
		policyAuthority.PurgeProject(projectID)
	})

	// No IronProxyConfigPath file is ever written for this project —
	// loadIronProxyInfoFromConfig must fail with ENOENT.
	require.NoError(t, WriteStateSnapshot(identity.Prod, projectID, StateSnapshot{
		Cfg: schema.Config{
			Network: schema.Network{Allow: []schema.AllowEntry{{Host: "example.com"}}},
		},
	}))

	sup := supervisor.New(t.TempDir())
	adoptOneIronProxy(context.Background(), identity.Prod, sup, tart.New(), NewRoutes(), DiscoveredIronProxy{PID: 424242, ProjectID: projectID}, map[string]bool{projectID: true})

	// With no prior ironProxyState entry, an unreadable config, and no
	// ProjectIP in the snapshot to restore, no entry must be created at
	// all — a bare zero-port/zero-everything entry would make
	// discoverSoftnet push a FORWARDING rule at HostLoopIP:0 and make
	// healIronProxies' watchdog treat a perfectly healthy adopted proxy
	// as ProxyMissing and try to kill+respawn it.
	_, ok := ironProxyState.get(projectID)
	assert.False(t, ok, "no ironProxyState entry must be created when the config is unreadable and the snapshot has no ProjectIP")

	sockPath, err := IronPolicySocketPath(identity.Prod, projectID)
	require.NoError(t, err)
	client := dialPolicy(t, sockPath)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := client.TransformRequest(ctx, policyReq("example.com", "GET", "/"))
	require.NoError(t, err, "policy socket must still be served despite the unreadable config")
	require.Equal(t, transformv1.TransformAction_TRANSFORM_ACTION_CONTINUE, resp.GetAction())
	resp, err = client.TransformRequest(ctx, policyReq("blocked.example", "GET", "/"))
	require.NoError(t, err)
	require.Equal(t, transformv1.TransformAction_TRANSFORM_ACTION_REJECT, resp.GetAction())
}

// TestAdoptOneIronProxy_UnreadableConfig_ProjectIPStillRestored covers
// the other half of the ironProxyState contract for an unreadable
// config: when the snapshot DOES carry a ProjectIP, an entry must still
// be created (recoverProjectState's ordinary ProjectIP-restore path),
// even though the config-load failure means no ports get rehydrated.
func TestAdoptOneIronProxy_UnreadableConfig_ProjectIPStillRestored(t *testing.T) {
	const projectID = "adopt-no-config-ip-proj"
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(func() {
		ironProxyState.del(projectID)
		policyAuthority.PurgeProject(projectID)
	})

	require.NoError(t, WriteStateSnapshot(identity.Prod, projectID, StateSnapshot{
		Cfg:       schema.Config{Project: schema.Project{Name: projectID}},
		ProjectIP: "127.42.0.11",
	}))

	sup := supervisor.New(t.TempDir())
	adoptOneIronProxy(context.Background(), identity.Prod, sup, tart.New(), NewRoutes(), DiscoveredIronProxy{PID: 424243, ProjectID: projectID}, map[string]bool{projectID: true})

	info, ok := ironProxyState.get(projectID)
	require.True(t, ok, "an entry must be created to carry the restored ProjectIP")
	assert.Equal(t, "127.42.0.11", info.ProjectIP)
	assert.Zero(t, info.HTTPPort, "ports must not be recovered when the config file is unreadable")
}

// TestRecoverProjectState_PreservesRouteModeAcrossRestart pins that a
// project last put into `devm route local` mode still comes back as
// ModeLocal after a daemon restart. The recovery path replays snap.Routes
// verbatim, so whatever mode the CLI last posted survives — no silent
// flip back to ModeVM.
func TestRecoverProjectState_PreservesRouteModeAcrossRestart(t *testing.T) {
	const projectID = "recover-mode-proj"
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(func() {
		ironProxyState.del(projectID)
		policyAuthority.PurgeProject(projectID)
	})

	require.NoError(t, WriteStateSnapshot(identity.Prod, projectID, StateSnapshot{
		Cfg: schema.Config{Project: schema.Project{Name: projectID}},
		Routes: []Route{
			{Hostname: "api.recover-mode-proj.test", BackendPort: 8080, Mode: ModeLocal, Project: projectID},
		},
	}))

	routes := NewRoutes()
	recoverProjectState(context.Background(), identity.Prod, tart.New(), routes, projectID)

	rt, ok := routes.Lookup("api.recover-mode-proj.test", projectID)
	require.True(t, ok)
	assert.Equal(t, ModeLocal, rt.Mode, "last-applied route mode must survive daemon restart")
	assert.Empty(t, rt.BackendHost, "local-mode route dials Mac localhost — BackendHost stays empty")
}

// TestRecoverProjectState_MissingSnapshot_LeavesStateUntouched covers a
// project whose config was never written to disk (or the snapshot is
// malformed) — recoverProjectState has nothing to restore or rebuild,
// so it must return without touching the pre-seeded ironProxyState
// entry or the routes table.
func TestRecoverProjectState_MissingSnapshot_LeavesStateUntouched(t *testing.T) {
	const projectID = "no-snapshot-proj"
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(func() {
		ironProxyState.del(projectID)
		policyAuthority.PurgeProject(projectID)
	})

	seeded := projectInfo{HTTPPort: 111, HTTPSPort: 222, DNSPort: 333}
	ironProxyState.put(projectID, seeded)

	routes := NewRoutes()
	recoverProjectState(context.Background(), identity.Prod, tart.New(), routes, projectID)

	info, ok := ironProxyState.get(projectID)
	assert.True(t, ok)
	assert.Equal(t, seeded, info, "no snapshot means nothing to restore — entry must be untouched")

	assert.Empty(t, routes.AllByProject()[projectID])
}

// TestRecoverProjectState_NoPriorEntry_SnapshotStillAppliesRoutes covers
// the defensive case where ironProxyState holds no entry yet for the
// project (e.g. called outside AdoptIronProxies's normal
// config-rehydration-first order): given only a state snapshot,
// recoverProjectState must still create an entry and replay the
// snapshot's routes.
func TestRecoverProjectState_NoPriorEntry_SnapshotStillAppliesRoutes(t *testing.T) {
	const projectID = "vm-down-proj"
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(func() {
		ironProxyState.del(projectID)
		policyAuthority.PurgeProject(projectID)
	})

	require.NoError(t, WriteStateSnapshot(identity.Prod, projectID, StateSnapshot{
		Cfg: schema.Config{Project: schema.Project{Name: projectID}},
		Routes: []Route{
			{Hostname: "db.vm-down-proj.test", BackendPort: 5432, Mode: ModeVM, Direct: true, Project: projectID},
		},
	}))

	routes := NewRoutes()
	recoverProjectState(context.Background(), identity.Prod, tart.New(), routes, projectID)

	_, ok := ironProxyState.get(projectID)
	assert.True(t, ok)

	route, ok := findRoute(routes, "db.vm-down-proj.test")
	require.True(t, ok)
	assert.True(t, route.Direct, "the direct flag must survive the snapshot round-trip")
	assert.Equal(t, projectID, route.Project)
}

// TestRecoverProjectState_RestoresProjectIP covers the daemon-restart
// adoption gap for the allocated project IP: ProjectIP isn't part of
// iron-proxy's on-disk config, so without this recovery step a daemon
// restart would strand a running project without its 127.42.0.x address
// and AllocateProjectIP would hand out a second one on the next
// /vm/start.
func TestRecoverProjectState_RestoresProjectIP(t *testing.T) {
	const projectID = "recover-ip-proj"
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(func() {
		ironProxyState.del(projectID)
		policyAuthority.PurgeProject(projectID)
	})

	ironProxyState.put(projectID, projectInfo{HTTPPort: 59481, HTTPSPort: 59482, DNSPort: 59483})

	require.NoError(t, WriteStateSnapshot(identity.Prod, projectID, StateSnapshot{
		Cfg:       schema.Config{Project: schema.Project{Name: projectID}},
		ProjectIP: "127.42.0.7",
	}))

	routes := NewRoutes()
	recoverProjectState(context.Background(), identity.Prod, tart.New(), routes, projectID)

	info, ok := ironProxyState.get(projectID)
	require.True(t, ok)
	assert.Equal(t, "127.42.0.7", info.ProjectIP)
	assert.Equal(t, 59481, info.HTTPPort, "pre-seeded ports must survive the ProjectIP restore")
}

func TestLoadIronProxyInfoFromConfig_MalformedListen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
dns:
  listen: "not-a-hostport"
proxy:
  http_listen: "192.168.64.1:80"
  https_listen: "192.168.64.1:443"
`), 0600))
	_, err := loadIronProxyInfoFromConfig(path)
	assert.Error(t, err)
}

// TestRecoverProjectState_SetsRestrictedMode verifies that on daemon
// startup, recoverProjectState (called per adopted project) forces the
// project back to restricted mode — the fail-safe default. This ensures
// that even if a project was left in passthrough mode when the daemon
// died, recovery resets it to restricted on the next startup.
func TestRecoverProjectState_SetsRestrictedMode(t *testing.T) {
	const projectID = "recover-restrict-proj"
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(func() { ironProxyState.del(projectID) })

	// Seed the authority with passthrough mode BEFORE recovery, to prove
	// recovery actively resets it (not just leaves it at the default).
	// tempAuth is captured in a local so cleanup can purge it directly:
	// t.Cleanup callbacks run AFTER this function's own defers, so by
	// the time a cleanup fires the `defer` below has already restored
	// the package-level policyAuthority back to orig — a cleanup that
	// referenced the package-level var would purge orig (a no-op) and
	// leak tempAuth's grpc listener and socket file.
	orig := policyAuthority
	tempAuth := NewPolicyAuthority()
	t.Cleanup(func() { tempAuth.PurgeProject(projectID) })
	policyAuthority = tempAuth
	policyAuthority.SetMode(projectID, ModePassthrough)
	defer func() { policyAuthority = orig }()

	// Create a state snapshot with the project's allowlist.
	require.NoError(t, WriteStateSnapshot(identity.Prod, projectID, StateSnapshot{
		Cfg: schema.Config{
			Network: schema.Network{Allow: []schema.AllowEntry{{Host: "github.com"}}},
		},
	}))

	recoverProjectState(context.Background(), identity.Prod, tart.New(), NewRoutes(), projectID)

	if got := policyAuthority.modeFor(projectID); got != ModeRestricted {
		t.Fatalf("recovery should force mode=restricted; got %v", got)
	}
}
