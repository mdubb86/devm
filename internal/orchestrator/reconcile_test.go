package orchestrator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func reconcileMinimalCfg() schema.Config {
	return schema.Config{
		Project: schema.Project{ID: "x", SandboxName: "x"},
	}
}

// makeFakeTart writes a fake tart binary that:
//   - `list --format json` → emits listJSON (passed as a file to avoid quoting issues)
//   - `exec <vmName> cat <path>` → emits snapOut (passed as a file)
//   - all other exec calls → exits 0 (snapshot writes, probes, dispatch)
func makeFakeTart(t *testing.T, dir, listJSON, snapOut string) *tart.Tart {
	t.Helper()
	bin := filepath.Join(dir, "fake-tart")
	listFile := filepath.Join(dir, "list.json")
	snapFile := filepath.Join(dir, "snap.yaml")

	require.NoError(t, os.WriteFile(listFile, []byte(listJSON), 0o644))
	require.NoError(t, os.WriteFile(snapFile, []byte(snapOut), 0o644))

	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  list)\n" +
		"    cat '" + listFile + "'\n" +
		"    ;;\n" +
		"  exec)\n" +
		"    case \"$3\" in\n" +
		"      cat)\n" +
		"        cat '" + snapFile + "'\n" +
		"        ;;\n" +
		"      *)\n" +
		"        exit 0\n" +
		"        ;;\n" +
		"    esac\n" +
		"    ;;\n" +
		"  *)\n" +
		"    exit 0\n" +
		"    ;;\n" +
		"esac\n"
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
	tr := tart.New()
	tr.Path = bin
	return tr
}

func runningVMListJSON(vmName string) string {
	return `[{"Name":"` + vmName + `","State":"running"}]`
}

func absentVMListJSON() string { return `[]` }

func TestRunReconcileInner_NothingToDo(t *testing.T) {
	cfg := reconcileMinimalCfg()
	snapYAML, _ := yaml.Marshal(cfg)
	tr := makeFakeTart(t, t.TempDir(), runningVMListJSON("x"), string(snapYAML))
	res, err := RunReconcileInner(cfg, tr, "x", "/tmp/fake-repo-root")
	assert.NoError(t, err)
	assert.Empty(t, res.Applied)
	assert.Empty(t, res.RecreateRequired)
	assert.Equal(t, "nothing_to_do", res.NextAction)
}

func TestRunReconcileInner_LivePortAdd(t *testing.T) {
	snapCfg := reconcileMinimalCfg()
	snapYAML, _ := yaml.Marshal(snapCfg)
	tr := makeFakeTart(t, t.TempDir(), runningVMListJSON("x"), string(snapYAML))

	newCfg := reconcileMinimalCfg()
	newCfg.Services = map[string]schema.Service{"api": {Port: 8080}}

	res, err := RunReconcileInner(newCfg, tr, "x", "/tmp/fake-repo-root")
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
	tr := makeFakeTart(t, t.TempDir(), runningVMListJSON("x"), string(snapYAML))

	newCfg := reconcileMinimalCfg()
	newCfg.Install = []string{"new-install"}

	res, err := RunReconcileInner(newCfg, tr, "x", "/tmp/fake-repo-root")
	assert.NoError(t, err)
	assert.Empty(t, res.Applied)
	assert.Len(t, res.RecreateRequired, 1)
	assert.Equal(t, FlavorTeardownShell, res.Flavor)
	assert.Equal(t, "needs_approval", res.NextAction)
}

func TestRunReconcileInner_EmptySnapshotIsIdentityWithNew(t *testing.T) {
	// No snapshot in VM → treat as same as new (no changes detected;
	// snapshot will be written at the end so future reconciles diff).
	tr := makeFakeTart(t, t.TempDir(), runningVMListJSON("x"), "")

	cfg := reconcileMinimalCfg()
	cfg.Services = map[string]schema.Service{"api": {Port: 8080}}

	res, err := RunReconcileInner(cfg, tr, "x", "/tmp/fake-repo-root")
	assert.NoError(t, err)
	assert.Empty(t, res.Applied)
	assert.Empty(t, res.RecreateRequired)
	assert.Equal(t, "nothing_to_do", res.NextAction)
}

func TestRunReconcileInner_LiveOnly_WritesSnapshot(t *testing.T) {
	snapCfg := reconcileMinimalCfg()
	snapYAML, _ := yaml.Marshal(snapCfg)
	tr := makeFakeTart(t, t.TempDir(), runningVMListJSON("x"), string(snapYAML))

	newCfg := reconcileMinimalCfg()
	newCfg.Services = map[string]schema.Service{"api": {Port: 8080}}

	_, err := RunReconcileInner(newCfg, tr, "x", "/tmp/fake-repo-root")
	assert.NoError(t, err)
}

func TestRunReconcileInner_RecreatePending_DoesNotError(t *testing.T) {
	snapCfg := reconcileMinimalCfg()
	snapCfg.Install = []string{"old"}
	snapYAML, _ := yaml.Marshal(snapCfg)
	tr := makeFakeTart(t, t.TempDir(), runningVMListJSON("x"), string(snapYAML))

	newCfg := reconcileMinimalCfg()
	newCfg.Install = []string{"new"}

	res, err := RunReconcileInner(newCfg, tr, "x", "/tmp/fake-repo-root")
	assert.NoError(t, err)
	assert.Equal(t, "needs_approval", res.NextAction)
}

// ---------------------------------------------------------------------------
// RunReconcile (outer state machine) tests.
// ---------------------------------------------------------------------------

func TestRunReconcile_StoppedSandboxRendersAndExits0(t *testing.T) {
	tr := makeFakeTart(t, t.TempDir(), absentVMListJSON(), "")
	cfg := reconcileMinimalCfg()
	opts := ReconcileOptions{}
	repoRoot := t.TempDir()
	rc, res, err := RunReconcile(cfg, tr, repoRoot, opts)
	assert.NoError(t, err)
	assert.Equal(t, 0, rc)
	assert.Equal(t, "nothing_to_do", res.NextAction)
	// .devm/.env should exist after the render step.
	_, statErr := os.Stat(filepath.Join(repoRoot, ".devm", ".env"))
	assert.NoError(t, statErr, ".devm/.env must be written even when sandbox is absent")
}

func TestRunReconcile_NonTTYRecreateExits2(t *testing.T) {
	// Snapshot has install A; new cfg has install B → recreate required.
	// Non-TTY, no --yes → exit code 2, NextAction needs_approval.
	snapCfg := reconcileMinimalCfg()
	snapCfg.Install = []string{"old"}
	snapYAML, _ := yaml.Marshal(snapCfg)
	tr := makeFakeTart(t, t.TempDir(), runningVMListJSON("x-sbx"), string(snapYAML))
	newCfg := reconcileMinimalCfg()
	newCfg.Project.SandboxName = "x-sbx"
	newCfg.Install = []string{"new"}
	opts := ReconcileOptions{NonInteractive: true}
	rc, res, err := RunReconcile(newCfg, tr, t.TempDir(), opts)
	assert.NoError(t, err)
	assert.Equal(t, 2, rc)
	assert.Equal(t, "needs_approval", res.NextAction)
}

func TestRunReconcile_DryRunDoesNotApply(t *testing.T) {
	snapCfg := reconcileMinimalCfg()
	snapCfg.Project.SandboxName = "x-sbx"
	snapYAML, _ := yaml.Marshal(snapCfg)
	tr := makeFakeTart(t, t.TempDir(), runningVMListJSON("x-sbx"), string(snapYAML))
	newCfg := reconcileMinimalCfg()
	newCfg.Project.SandboxName = "x-sbx"
	newCfg.Services = map[string]schema.Service{"api": {Port: 8080}}
	opts := ReconcileOptions{DryRun: true}
	rc, res, err := RunReconcile(newCfg, tr, t.TempDir(), opts)
	assert.NoError(t, err)
	assert.Equal(t, 0, rc)
	assert.Len(t, res.Applied, 1, "diff should still be computed for dry-run")
	assert.Equal(t, KindPortAdd, res.Applied[0].Kind)
}
