package orchestrator

import (
	"testing"

	"github.com/mtwaage/devm/internal/sandbox"
	"github.com/mtwaage/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func statusMinimalCfg() schema.Config {
	return schema.Config{
		Project: schema.Project{ID: "x", SandboxName: "x-sbx", HostnameApex: "x.local", PortOffset: 50000},
	}
}

func TestRunStatus_Absent(t *testing.T) {
	r := &stateRunner{lsAbsent: true}
	sb := &sandbox.Sandbox{Name: "x-sbx", Runner: r}
	res, err := RunStatus(statusMinimalCfg(), sb, "/tmp/fake")
	assert.NoError(t, err)
	assert.Equal(t, "absent", res.State)
	assert.Empty(t, res.Sessions)
	assert.Equal(t, "x-sbx", res.Sandbox)
}

func TestRunStatus_Stopped(t *testing.T) {
	r := &stateRunner{lsStatus: "stopped"}
	sb := &sandbox.Sandbox{Name: "x-sbx", Runner: r}
	res, err := RunStatus(statusMinimalCfg(), sb, "/tmp/fake")
	assert.NoError(t, err)
	assert.Equal(t, "stopped", res.State)
	assert.Empty(t, res.Sessions)
}

func TestRunStatus_RunningInSync(t *testing.T) {
	snapCfg := statusMinimalCfg()
	snapYAML, _ := yaml.Marshal(snapCfg)
	r := &stateRunner{
		lsStatus: "running",
		catOut:   string(snapYAML),
		probeOut: "27 bash pts/1 agent\n",
	}
	sb := &sandbox.Sandbox{Name: "x-sbx", Runner: r}
	res, err := RunStatus(snapCfg, sb, "/tmp/fake")
	assert.NoError(t, err)
	assert.Equal(t, "running", res.State)
	assert.Len(t, res.Sessions, 1)
	assert.Zero(t, res.PendingLive)
	assert.Zero(t, res.PendingRecreate)
}

func TestRunStatus_RunningPendingMixed(t *testing.T) {
	snapCfg := statusMinimalCfg()
	snapCfg.Install = []string{"old"}
	snapYAML, _ := yaml.Marshal(snapCfg)
	r := &stateRunner{
		lsStatus: "running",
		catOut:   string(snapYAML),
		probeOut: "",
	}
	sb := &sandbox.Sandbox{Name: "x-sbx", Runner: r}
	newCfg := statusMinimalCfg()
	newCfg.Install = []string{"new"}
	newCfg.Services = map[string]schema.Service{"api": {Canonical: 8080}}
	res, err := RunStatus(newCfg, sb, "/tmp/fake")
	assert.NoError(t, err)
	assert.Equal(t, 1, res.PendingLive)     // port_add
	assert.Equal(t, 1, res.PendingRecreate) // install_change
}

func TestRunStatus_RunningEmptySnapshotIsInSync(t *testing.T) {
	// Empty snapshot in VM → treat as identical to new cfg.
	r := &stateRunner{
		lsStatus: "running",
		catOut:   "",
		probeOut: "",
	}
	sb := &sandbox.Sandbox{Name: "x-sbx", Runner: r}
	cfg := statusMinimalCfg()
	cfg.Services = map[string]schema.Service{"api": {Canonical: 8080}}
	res, err := RunStatus(cfg, sb, "/tmp/fake")
	assert.NoError(t, err)
	assert.Equal(t, "running", res.State)
	assert.Zero(t, res.PendingLive)
	assert.Zero(t, res.PendingRecreate)
}

func TestRunStatusLive_StoppedNoDrift(t *testing.T) {
	// Not running → no live state to compare → no drift.
	r := &stateRunner{lsStatus: "stopped"}
	sb := &sandbox.Sandbox{Name: "x-sbx", Runner: r}
	res, err := RunStatusLive(statusMinimalCfg(), sb, "/tmp/fake")
	assert.NoError(t, err)
	assert.Empty(t, res.Drift)
}

func TestRunStatusLive_PortMissingDrift(t *testing.T) {
	// Snapshot expects api on 8080 (host 58080), but live ports are
	// empty → port_missing drift.
	snapCfg := statusMinimalCfg()
	snapCfg.Services = map[string]schema.Service{"api": {Canonical: 8080}}
	snapYAML, _ := yaml.Marshal(snapCfg)
	r := &stateRunner{
		lsStatus: "running",
		catOut:   string(snapYAML),
		portsOut: "[]", // nothing published live
	}
	sb := &sandbox.Sandbox{Name: "x-sbx", Runner: r}
	res, err := RunStatusLive(snapCfg, sb, "/tmp/fake")
	assert.NoError(t, err)
	require.Len(t, res.Drift, 1)
	assert.Equal(t, "port_missing", res.Drift[0].Kind)
	assert.Contains(t, res.Drift[0].Detail, "58080")
}

func TestRunStatusLive_PortExtraDrift(t *testing.T) {
	// Snapshot expects nothing, but live has a published port →
	// port_extra drift.
	snapCfg := statusMinimalCfg()
	snapYAML, _ := yaml.Marshal(snapCfg)
	r := &stateRunner{
		lsStatus: "running",
		catOut:   string(snapYAML),
		portsOut: `[{"host_ip":"127.0.0.1","host_port":59090,"sandbox_port":9090,"protocol":"tcp"}]`,
	}
	sb := &sandbox.Sandbox{Name: "x-sbx", Runner: r}
	res, err := RunStatusLive(snapCfg, sb, "/tmp/fake")
	assert.NoError(t, err)
	require.Len(t, res.Drift, 1)
	assert.Equal(t, "port_extra", res.Drift[0].Kind)
	assert.Contains(t, res.Drift[0].Detail, "9090")
}

func TestRunStatusLive_InSyncNoDrift(t *testing.T) {
	// Snapshot expects api 8080 (host 58080) and live has exactly that.
	snapCfg := statusMinimalCfg()
	snapCfg.Services = map[string]schema.Service{"api": {Canonical: 8080}}
	snapYAML, _ := yaml.Marshal(snapCfg)
	r := &stateRunner{
		lsStatus: "running",
		catOut:   string(snapYAML),
		portsOut: `[{"host_ip":"127.0.0.1","host_port":58080,"sandbox_port":8080,"protocol":"tcp"}]`,
	}
	sb := &sandbox.Sandbox{Name: "x-sbx", Runner: r}
	res, err := RunStatusLive(snapCfg, sb, "/tmp/fake")
	assert.NoError(t, err)
	assert.Empty(t, res.Drift)
}
