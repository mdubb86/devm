package orchestrator

import (
	"testing"

	"github.com/mtwaage/devm/internal/sandbox"
	"github.com/mtwaage/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"gopkg.in/yaml.v3"
)

func reconcileMinimalCfg() schema.Config {
	return schema.Config{
		Project: schema.Project{ID: "x", SandboxName: "x", HostnameApex: "x.local", PortOffset: 50000},
	}
}

func TestRunReconcileInner_NothingToDo(t *testing.T) {
	cfg := reconcileMinimalCfg()
	snapYAML, _ := yaml.Marshal(cfg)
	r := &stubRunner{outputOut: string(snapYAML)}
	sb := &sandbox.Sandbox{Name: "x", Runner: r}
	res, err := RunReconcileInner(cfg, sb, "/tmp/fake-repo-root")
	assert.NoError(t, err)
	assert.Empty(t, res.Applied)
	assert.Empty(t, res.RecreateRequired)
	assert.Equal(t, "nothing_to_do", res.NextAction)
}

func TestRunReconcileInner_LivePortAdd(t *testing.T) {
	snapCfg := reconcileMinimalCfg()
	snapYAML, _ := yaml.Marshal(snapCfg)
	r := &stubRunner{outputOut: string(snapYAML)}
	sb := &sandbox.Sandbox{Name: "x", Runner: r}

	newCfg := reconcileMinimalCfg()
	newCfg.Services = map[string]schema.Service{"api": {Canonical: 8080}}

	res, err := RunReconcileInner(newCfg, sb, "/tmp/fake-repo-root")
	assert.NoError(t, err)
	assert.Len(t, res.Applied, 1)
	assert.Equal(t, KindPortAdd, res.Applied[0].Kind)
	assert.Empty(t, res.RecreateRequired)
	assert.Equal(t, "applied", res.NextAction)
}

func TestRunReconcileInner_RecreateRequired(t *testing.T) {
	snapCfg := reconcileMinimalCfg()
	snapCfg.Install = []string{"old-install"}
	snapYAML, _ := yaml.Marshal(snapCfg)
	r := &stubRunner{outputOut: string(snapYAML)}
	sb := &sandbox.Sandbox{Name: "x", Runner: r}

	newCfg := reconcileMinimalCfg()
	newCfg.Install = []string{"new-install"}

	res, err := RunReconcileInner(newCfg, sb, "/tmp/fake-repo-root")
	assert.NoError(t, err)
	assert.Empty(t, res.Applied)
	assert.Len(t, res.RecreateRequired, 1)
	assert.Equal(t, FlavorTeardownShell, res.Flavor)
	assert.Equal(t, "needs_approval", res.NextAction)
}

func TestRunReconcileInner_EmptySnapshotIsIdentityWithNew(t *testing.T) {
	// No snapshot in VM → treat as same as new (no changes detected;
	// snapshot will be written at the end so future reconciles diff).
	r := &stubRunner{outputOut: ""} // empty
	sb := &sandbox.Sandbox{Name: "x", Runner: r}

	cfg := reconcileMinimalCfg()
	cfg.Services = map[string]schema.Service{"api": {Canonical: 8080}}

	res, err := RunReconcileInner(cfg, sb, "/tmp/fake-repo-root")
	assert.NoError(t, err)
	assert.Empty(t, res.Applied)
	assert.Empty(t, res.RecreateRequired)
	assert.Equal(t, "nothing_to_do", res.NextAction)
	// Snapshot SHOULD have been written.
	assert.NotEmpty(t, r.runStdinSeen, "snapshot should be written when no recreate pending")
	assert.Contains(t, r.runStdinSeen, "devm snapshot")
}

func TestRunReconcileInner_LiveOnly_WritesSnapshot(t *testing.T) {
	snapCfg := reconcileMinimalCfg()
	snapYAML, _ := yaml.Marshal(snapCfg)
	r := &stubRunner{outputOut: string(snapYAML)}
	sb := &sandbox.Sandbox{Name: "x", Runner: r}

	newCfg := reconcileMinimalCfg()
	newCfg.Services = map[string]schema.Service{"api": {Canonical: 8080}}

	_, err := RunReconcileInner(newCfg, sb, "/tmp/fake-repo-root")
	assert.NoError(t, err)
	assert.NotEmpty(t, r.runStdinSeen, "snapshot must be written after successful LIVE apply")
}

func TestRunReconcileInner_RecreatePending_DoesNotWriteSnapshot(t *testing.T) {
	snapCfg := reconcileMinimalCfg()
	snapCfg.Install = []string{"old"}
	snapYAML, _ := yaml.Marshal(snapCfg)
	r := &stubRunner{outputOut: string(snapYAML)}
	sb := &sandbox.Sandbox{Name: "x", Runner: r}

	newCfg := reconcileMinimalCfg()
	newCfg.Install = []string{"new"}

	_, err := RunReconcileInner(newCfg, sb, "/tmp/fake-repo-root")
	assert.NoError(t, err)
	assert.Empty(t, r.runStdinSeen, "snapshot must NOT be written when recreate is pending")
}
