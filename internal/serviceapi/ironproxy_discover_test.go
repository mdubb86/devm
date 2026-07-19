package serviceapi

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
		HTTPListen:  "192.168.64.1:59481",
		HTTPSListen: "192.168.64.1:59482",
		DNSListen:   "192.168.64.1:59483",
		DNSProxyIP:  "192.0.2.1",
		CACertPath:  "/tmp/ca.crt",
		CAKeyPath:   "/tmp/ca.key",
	}
	blob, err := cfg.YAML()
	require.NoError(t, err)

	dir := t.TempDir()
	path := filepath.Join(dir, "proj.yaml")
	require.NoError(t, os.WriteFile(path, blob, 0600))

	info, err := loadIronProxyInfoFromConfig(path)
	require.NoError(t, err)
	assert.Equal(t, projectInfo{
		HTTPPort:  59481,
		HTTPSPort: 59482,
		DNSPort:   59483,
	}, info)
}

func TestLoadIronProxyInfoFromConfig_MissingFile(t *testing.T) {
	_, err := loadIronProxyInfoFromConfig("/nonexistent/nowhere.yaml")
	assert.Error(t, err)
}

// TestRecoverProjectState_RestoresSSHHostPortAndRebuildsDirectRoutes
// covers the daemon-restart adoption path (AdoptIronProxies calls this
// per recovered project, after already seeding ironProxyState from the
// project's on-disk iron-proxy config): given a state snapshot on disk
// describing a direct service and an SSH host port, recoverProjectState
// should restore SSHHostPort onto the pre-seeded entry and rebuild the
// project's direct routes (so DNS keeps answering for it) — all without
// a daemon restart actually having happened.
func TestRecoverProjectState_RestoresSSHHostPortAndRebuildsDirectRoutes(t *testing.T) {
	const projectID = "recover-proj"
	t.Setenv("DEVM_RUNTIME_DIR", t.TempDir())
	t.Cleanup(func() { ironProxyState.del(projectID) })

	// Mirrors AdoptIronProxies having already rehydrated ironProxyState
	// from the project's on-disk iron-proxy config before calling
	// recoverProjectState.
	ironProxyState.put(projectID, projectInfo{HTTPPort: 59481, HTTPSPort: 59482, DNSPort: 59483})

	snap := StateSnapshot{
		Cfg: schema.Config{
			Project: schema.Project{Name: projectID},
			Services: map[string]schema.Service{
				"db": {
					Hostname: "db.test",
					Port:     5432,
					Direct:   true,
				},
				"web": {
					// Proxied (non-direct) service; must NOT show up as a
					// direct route.
					Hostname: "web.test",
					Port:     3000,
				},
			},
		},
		SSHHostPort: 2201,
	}
	require.NoError(t, WriteStateSnapshot(projectID, snap))

	routes := NewRoutes()
	recoverProjectState(context.Background(), tart.New(), routes, projectID)

	info, ok := ironProxyState.get(projectID)
	assert.True(t, ok)
	assert.Equal(t, 2201, info.SSHHostPort, "SSHHostPort must be restored from the state snapshot")
	assert.Equal(t, 59481, info.HTTPPort, "pre-seeded ports must survive the SSHHostPort restore")

	route, ok := routes.DirectRoute("db.test")
	require.True(t, ok)
	assert.Equal(t, 5432, route.BackendPort)
	assert.Equal(t, projectID, route.Project)

	_, ok = routes.DirectRoute("web.test")
	assert.False(t, ok, "non-direct service must not become a direct route")
}

// TestRecoverProjectState_MissingSnapshot_LeavesStateUntouched covers a
// project whose config was never written to disk (or the snapshot is
// malformed) — recoverProjectState has nothing to restore or rebuild,
// so it must return without touching the pre-seeded ironProxyState
// entry or the routes table.
func TestRecoverProjectState_MissingSnapshot_LeavesStateUntouched(t *testing.T) {
	const projectID = "no-snapshot-proj"
	t.Setenv("DEVM_RUNTIME_DIR", t.TempDir())
	t.Cleanup(func() { ironProxyState.del(projectID) })

	seeded := projectInfo{HTTPPort: 111, HTTPSPort: 222, DNSPort: 333}
	ironProxyState.put(projectID, seeded)

	routes := NewRoutes()
	recoverProjectState(context.Background(), tart.New(), routes, projectID)

	info, ok := ironProxyState.get(projectID)
	assert.True(t, ok)
	assert.Equal(t, seeded, info, "no snapshot means nothing to restore — entry must be untouched")

	assert.Empty(t, routes.AllByProject()[projectID])
}

// TestRecoverProjectState_NoPriorEntry_SnapshotStillAppliesSSHHostPortAndRoutes
// covers the defensive case where ironProxyState holds no entry yet for
// the project (e.g. called outside AdoptIronProxies's normal
// config-rehydration-first order): given only a state snapshot,
// recoverProjectState must still create an entry carrying SSHHostPort
// and rebuild direct routes.
func TestRecoverProjectState_NoPriorEntry_SnapshotStillAppliesSSHHostPortAndRoutes(t *testing.T) {
	const projectID = "vm-down-proj"
	t.Setenv("DEVM_RUNTIME_DIR", t.TempDir())
	t.Cleanup(func() { ironProxyState.del(projectID) })

	require.NoError(t, WriteStateSnapshot(projectID, StateSnapshot{
		Cfg: schema.Config{
			Project: schema.Project{Name: projectID},
			Services: map[string]schema.Service{
				"db": {Hostname: "db.test", Port: 5432, Direct: true},
			},
		},
		SSHHostPort: 2202,
	}))

	routes := NewRoutes()
	recoverProjectState(context.Background(), tart.New(), routes, projectID)

	info, ok := ironProxyState.get(projectID)
	assert.True(t, ok)
	assert.Equal(t, 2202, info.SSHHostPort)

	route, ok := routes.DirectRoute("db.test")
	require.True(t, ok)
	assert.Equal(t, projectID, route.Project)
}

// TestRecoverProjectState_RestoresProjectIP covers the same
// daemon-restart adoption gap as the SSHHostPort recovery test above,
// but for the allocated project IP: like SSHHostPort, ProjectIP isn't
// part of iron-proxy's on-disk config, so without this recovery step a
// daemon restart would strand a running project without its
// 127.42.0.x address and AllocateProjectIP would hand out a second one
// on the next /vm/start.
func TestRecoverProjectState_RestoresProjectIP(t *testing.T) {
	const projectID = "recover-ip-proj"
	t.Setenv("DEVM_RUNTIME_DIR", t.TempDir())
	t.Cleanup(func() { ironProxyState.del(projectID) })

	ironProxyState.put(projectID, projectInfo{HTTPPort: 59481, HTTPSPort: 59482, DNSPort: 59483})

	require.NoError(t, WriteStateSnapshot(projectID, StateSnapshot{
		Cfg:       schema.Config{Project: schema.Project{Name: projectID}},
		ProjectIP: "127.42.0.7",
	}))

	routes := NewRoutes()
	recoverProjectState(context.Background(), tart.New(), routes, projectID)

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
