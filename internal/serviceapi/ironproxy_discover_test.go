package serviceapi

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
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
	}
	blob, err := cfg.YAML()
	require.NoError(t, err)

	dir := t.TempDir()
	path := filepath.Join(dir, "proj.yaml")
	require.NoError(t, os.WriteFile(path, blob, 0600))

	info, err := loadIronProxyInfoFromConfig(path)
	require.NoError(t, err)
	assert.Equal(t, projectInfo{
		HTTPPort:   59481,
		HTTPSPort:  59482,
		TunnelPort: 59484,
		DNSPort:    59483,
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
	t.Cleanup(func() { ironProxyState.del(projectID) })

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

	direct, ok := routes.DirectRoute("db.recover-proj.test")
	require.True(t, ok, "direct route must be replayed")
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

// TestRecoverProjectState_PreservesRouteModeAcrossRestart pins that a
// project last put into `devm route local` mode still comes back as
// ModeLocal after a daemon restart. The recovery path replays snap.Routes
// verbatim, so whatever mode the CLI last posted survives — no silent
// flip back to ModeVM.
func TestRecoverProjectState_PreservesRouteModeAcrossRestart(t *testing.T) {
	const projectID = "recover-mode-proj"
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(func() { ironProxyState.del(projectID) })

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
	t.Cleanup(func() { ironProxyState.del(projectID) })

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
	t.Cleanup(func() { ironProxyState.del(projectID) })

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

	route, ok := routes.DirectRoute("db.vm-down-proj.test")
	require.True(t, ok)
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
	t.Cleanup(func() { ironProxyState.del(projectID) })

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
