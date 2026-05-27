package orchestrator

import (
	"testing"

	"github.com/mtwaage/devm/internal/sandbox"
	"github.com/mtwaage/devm/internal/schema"
	"github.com/stretchr/testify/assert"
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

func TestRunStatusLive_IsAliasForRunStatusInV1(t *testing.T) {
	// v1: --live is stubbed to just call RunStatus and return empty Drift.
	r := &stateRunner{lsStatus: "stopped"}
	sb := &sandbox.Sandbox{Name: "x-sbx", Runner: r}
	a, _ := RunStatus(statusMinimalCfg(), sb, "/tmp/fake")
	b, _ := RunStatusLive(statusMinimalCfg(), sb, "/tmp/fake")
	assert.Equal(t, a.State, b.State)
	assert.Equal(t, a.PendingLive, b.PendingLive)
	assert.Empty(t, b.Drift)
}
