package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdubb86/devm/internal/sandbox"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"gopkg.in/yaml.v3"
)

func reconcileMinimalCfg() schema.Config {
	return schema.Config{
		Project: schema.Project{ID: "x", SandboxName: "x"},
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
	newCfg.Services = map[string]schema.Service{"api": {Port: 8080}}

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
	cfg.Services = map[string]schema.Service{"api": {Port: 8080}}

	res, err := RunReconcileInner(cfg, sb, "/tmp/fake-repo-root")
	assert.NoError(t, err)
	assert.Empty(t, res.Applied)
	assert.Empty(t, res.RecreateRequired)
	assert.Equal(t, "nothing_to_do", res.NextAction)
	// Snapshot SHOULD have been written (content embedded in argv).
	assert.True(t, sawSnapshotWriteCall(r), "snapshot should be written when no recreate pending")
}

func TestRunReconcileInner_LiveOnly_WritesSnapshot(t *testing.T) {
	snapCfg := reconcileMinimalCfg()
	snapYAML, _ := yaml.Marshal(snapCfg)
	r := &stubRunner{outputOut: string(snapYAML)}
	sb := &sandbox.Sandbox{Name: "x", Runner: r}

	newCfg := reconcileMinimalCfg()
	newCfg.Services = map[string]schema.Service{"api": {Port: 8080}}

	_, err := RunReconcileInner(newCfg, sb, "/tmp/fake-repo-root")
	assert.NoError(t, err)
	assert.True(t, sawSnapshotWriteCall(r), "snapshot must be written after successful LIVE apply")
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
	assert.False(t, sawSnapshotWriteCall(r), "snapshot must NOT be written when recreate is pending")
}

// sawSnapshotWriteCall returns true if the runner observed a
// WriteSnapshot invocation. Identifies the write by its distinctive
// argv shape (`base64 -d` + `mv ... applied.yaml.tmp` chain). Read
// calls also mention applied.yaml but use a plain `cat`, so this
// avoids false positives.
func sawSnapshotWriteCall(r *stubRunner) bool {
	for _, args := range r.lastArgs {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "base64 -d") && strings.Contains(joined, "applied.yaml.tmp") {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// RunReconcile (outer state machine) tests.
// ---------------------------------------------------------------------------

func TestRunReconcile_StoppedSandboxRendersAndExits0(t *testing.T) {
	// Sandbox absent — sbx ls returns no row for "x".
	r := &stateRunner{lsAbsent: true}
	sb := &sandbox.Sandbox{Name: "x", Runner: r}
	cfg := reconcileMinimalCfg()
	opts := ReconcileOptions{}
	repoRoot := t.TempDir()
	rc, res, err := RunReconcile(cfg, sb, repoRoot, opts)
	assert.NoError(t, err)
	assert.Equal(t, 0, rc)
	assert.Equal(t, "nothing_to_do", res.NextAction)
	// .devm/spec.yaml should exist after the render step.
	_, statErr := os.Stat(filepath.Join(repoRoot, ".devm", "spec.yaml"))
	assert.NoError(t, statErr, ".devm/spec.yaml must be rendered even when sandbox is absent")
}

func TestRunReconcile_NonTTYRecreateExits2(t *testing.T) {
	// Snapshot has install A; new cfg has install B → recreate required.
	// Non-TTY, no --yes → exit code 2, NextAction needs_approval.
	snapCfg := reconcileMinimalCfg()
	snapCfg.Install = []string{"old"}
	snapYAML, _ := yaml.Marshal(snapCfg)
	r := &stateRunner{
		lsStatus: "running",
		catOut:   string(snapYAML),
	}
	sb := &sandbox.Sandbox{Name: "x-sbx", Runner: r}
	newCfg := reconcileMinimalCfg()
	newCfg.Project.SandboxName = "x-sbx"
	newCfg.Install = []string{"new"}
	opts := ReconcileOptions{NonInteractive: true}
	rc, res, err := RunReconcile(newCfg, sb, t.TempDir(), opts)
	assert.NoError(t, err)
	assert.Equal(t, 2, rc)
	assert.Equal(t, "needs_approval", res.NextAction)
}

func TestRunReconcile_DryRunDoesNotApply(t *testing.T) {
	snapCfg := reconcileMinimalCfg()
	snapCfg.Project.SandboxName = "x-sbx"
	snapYAML, _ := yaml.Marshal(snapCfg)
	r := &stateRunner{
		lsStatus: "running",
		catOut:   string(snapYAML),
	}
	sb := &sandbox.Sandbox{Name: "x-sbx", Runner: r}
	newCfg := reconcileMinimalCfg()
	newCfg.Project.SandboxName = "x-sbx"
	newCfg.Services = map[string]schema.Service{"api": {Port: 8080}}
	opts := ReconcileOptions{DryRun: true}
	rc, res, err := RunReconcile(newCfg, sb, t.TempDir(), opts)
	assert.NoError(t, err)
	assert.Equal(t, 0, rc)
	// Dry-run computes diff but does NOT call sbx ports --publish.
	sawPublish := false
	for _, call := range r.calls {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "--publish") {
			sawPublish = true
		}
	}
	assert.False(t, sawPublish, "dry-run must not call sbx ports --publish")
	assert.Len(t, res.Applied, 1, "diff should still be computed for dry-run")
	assert.Equal(t, KindPortAdd, res.Applied[0].Kind)
}
