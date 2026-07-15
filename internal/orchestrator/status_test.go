package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/mdubb86/devm/internal/supervisor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func statusMinimalCfg() schema.Config {
	return schema.Config{
		Project: schema.Project{Name: "x"},
	}
}

// makeFakeTartStatus creates a fake tart binary for RunStatus tests.
// Uses files to avoid shell quoting issues with YAML content.
//   - `list --format json` → listJSON
//   - `exec <vmName> bash -c "cat ..."` → snapOut (ReadSnapshot)
//   - `exec <vmName> bash -c "for ..."` → probeOut (probeSessions)
func makeFakeTartStatus(t *testing.T, listJSON, snapOut, probeOut string) *tart.Tart {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-tart")
	listFile := filepath.Join(dir, "list.json")
	snapFile := filepath.Join(dir, "snap.yaml")
	probeFile := filepath.Join(dir, "probe.txt")

	require.NoError(t, os.WriteFile(listFile, []byte(listJSON), 0o644))
	require.NoError(t, os.WriteFile(snapFile, []byte(snapOut), 0o644))
	require.NoError(t, os.WriteFile(probeFile, []byte(probeOut), 0o644))

	// Both ReadSnapshot and probeSessions use `bash -c <script>`.
	// Distinguish them by $5 (the bash body):
	//   cat*  → ReadSnapshot ("cat \"$HOME/...\"")
	//   *     → probeSessions (the /proc-walking for-loop)
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  list)\n" +
		"    cat '" + listFile + "'\n" +
		"    ;;\n" +
		"  exec)\n" +
		"    case \"$3\" in\n" +
		"      bash)\n" +
		"        case \"$5\" in\n" +
		"          cat*)\n" +
		"            cat '" + snapFile + "'\n" +
		"            ;;\n" +
		"          *)\n" +
		"            cat '" + probeFile + "'\n" +
		"            ;;\n" +
		"        esac\n" +
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

func TestRunStatus_Absent(t *testing.T) {
	tr := makeFakeTartStatus(t, `[]`, "", "")
	res, err := RunStatus(statusMinimalCfg(), tr, "/tmp/fake", "test-fp")
	assert.NoError(t, err)
	assert.Equal(t, "absent", res.State)
	assert.Empty(t, res.Sessions)
	assert.Equal(t, "x", res.Sandbox)
}

func TestRunStatus_Stopped(t *testing.T) {
	tr := makeFakeTartStatus(t, `[{"Name":"x","State":"stopped"}]`, "", "")
	res, err := RunStatus(statusMinimalCfg(), tr, "/tmp/fake", "test-fp")
	assert.NoError(t, err)
	assert.Equal(t, "stopped", res.State)
	assert.Empty(t, res.Sessions)
}

func TestRunStatus_RunningInSync(t *testing.T) {
	snapCfg := statusMinimalCfg()
	snapYAML, _ := yaml.Marshal(snapCfg)
	tr := makeFakeTartStatus(t,
		`[{"Name":"x","State":"running"}]`,
		string(snapYAML),
		"27 bash pts/1 agent\n",
	)
	res, err := RunStatus(snapCfg, tr, "/tmp/fake", "test-fp")
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
	tr := makeFakeTartStatus(t,
		`[{"Name":"x","State":"running"}]`,
		string(snapYAML),
		"",
	)
	newCfg := statusMinimalCfg()
	newCfg.Install = []string{"new"}
	newCfg.Services = map[string]schema.Service{"api": {Port: 8080}}
	res, err := RunStatus(newCfg, tr, "/tmp/fake", "test-fp")
	assert.NoError(t, err)
	assert.Equal(t, 1, res.PendingLive)     // port_add
	assert.Equal(t, 1, res.PendingRecreate) // install_change
}

func TestRunStatus_RunningEmptySnapshotIsInSync(t *testing.T) {
	// Empty snapshot in VM → treat as identical to new cfg.
	tr := makeFakeTartStatus(t,
		`[{"Name":"x","State":"running"}]`,
		"",
		"",
	)
	cfg := statusMinimalCfg()
	cfg.Services = map[string]schema.Service{"api": {Port: 8080}}
	res, err := RunStatus(cfg, tr, "/tmp/fake", "test-fp")
	assert.NoError(t, err)
	assert.Equal(t, "running", res.State)
	assert.Zero(t, res.PendingLive)
	assert.Zero(t, res.PendingRecreate)
}

// startHandshakeDaemon spins up a real serviceapi.Server with the
// /handshake endpoint registered on a temp Unix socket, and points
// $DEVM_RUNTIME_DIR at a temp dir so serviceapi.SocketPath() (and
// therefore RunStatus's internal serviceapi.NewClient()) resolves to
// it. sup has no adopted iron-proxy PID for any project, so a
// handshake for any project_id reports ProxyMissing — the daemon is
// reachable, it just has nothing healthy to report. Returns a cleanup
// func.
func startHandshakeDaemon(t *testing.T) func() {
	t.Helper()
	// Unix domain socket paths are capped at ~104 bytes on macOS/BSD;
	// t.TempDir() nests too deep once "devm.sock" is appended. Use a
	// short /tmp-rooted dir instead.
	rtDir, err := os.MkdirTemp("/tmp", "devm-rt-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(rtDir) })
	t.Setenv("DEVM_RUNTIME_DIR", rtDir)

	socket := serviceapi.SocketPath()
	sup := supervisor.New("")
	srv := serviceapi.NewServer(socket, serviceapi.Build{Version: "test"})
	serviceapi.RegisterHandshakeHandler(srv, serviceapi.Build{Version: "test"}, sup)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.FileExists(t, socket)

	return func() { cancel(); <-errCh }
}

// TestRunStatus_StoppedVM_ProxyHealthNil proves the bug fix: with a
// reachable daemon that WOULD answer /handshake (it isn't unreachable
// — that's a separate nil-ProxyHealth path covered by
// TestRunStatus_RoutingZeroWhenDaemonUnreachable), a stopped-VM
// project still leaves res.ProxyHealth nil. Before the fix, RunStatus
// called Handshake unconditionally and this would have been non-nil
// (ProxyMissing), incorrectly triggering the `devm status` exit-4
// path for a VM that was never expected to have a live proxy.
func TestRunStatus_StoppedVM_ProxyHealthNil(t *testing.T) {
	cleanup := startHandshakeDaemon(t)
	defer cleanup()

	tr := makeFakeTartStatus(t, `[{"Name":"x","State":"stopped"}]`, "", "")
	res, err := RunStatus(statusMinimalCfg(), tr, "/tmp/fake", "test-fp")
	require.NoError(t, err)
	assert.Equal(t, "stopped", res.State)
	assert.Nil(t, res.ProxyHealth)
}

// TestRunStatus_RunningVM_MissingProxySet proves the running-VM path
// is unaffected: a running-VM project with no adopted iron-proxy still
// gets a non-nil ProxyHealth (MISSING), same as before the fix.
func TestRunStatus_RunningVM_MissingProxySet(t *testing.T) {
	cleanup := startHandshakeDaemon(t)
	defer cleanup()

	tr := makeFakeTartStatus(t, `[{"Name":"x","State":"running"}]`, "", "")
	res, err := RunStatus(statusMinimalCfg(), tr, "/tmp/fake", "test-fp")
	require.NoError(t, err)
	assert.Equal(t, "running", res.State)
	require.NotNil(t, res.ProxyHealth)
	assert.Equal(t, serviceapi.ProxyMissing, res.ProxyHealth.Status)
}

func TestRunStatus_RoutingZeroWhenDaemonUnreachable(t *testing.T) {
	// When the daemon is not running, RoutingStatusFromDaemon fails and
	// RunStatus leaves Routing zero-valued. RunStatus must not error out
	// in this case — the format layer handles zero Routing as unreachable.
	//
	// Point HOME at a tmpdir so serviceapi.SocketPath() resolves to a
	// nonexistent socket, simulating daemon-unreachable regardless of
	// whether a real daemon is running on this machine.
	t.Setenv("HOME", t.TempDir())

	tr := makeFakeTartStatus(t, `[]`, "", "")
	cfg := statusMinimalCfg()
	res, err := RunStatus(cfg, tr, "/tmp/fake", "test-fp")
	require.NoError(t, err)
	assert.Equal(t, "absent", res.State)
	assert.Equal(t, "", res.Routing.Proxy)
	assert.False(t, res.Routing.ProxyReachable)
}
