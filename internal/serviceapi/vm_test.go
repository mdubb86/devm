package serviceapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/supervisor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestVMStop_CallsMutagenStopPhaseBeforeGracefulStop verifies Task 18:
// the /vm/stop handler flushes+pauses the project's mutagen sessions
// (via mutagenStopPhaseFn) BEFORE gracefulStopVM powers the guest off —
// mutagen's transport is tart exec (cmd/tart-mutagen-ssh), which needs the
// guest running, and the poweroff ends that.
//
// mutagenStopPhaseFn is faked to append a marker into the same log file
// the fake tart binary writes every invocation into, so both events
// land on one ordered timeline — this pins sequencing, not StopPhase's
// own behavior (covered by mutagen_sessions_test.go).
//
// The fake tart binary reports the VM absent on `list` so
// gracefulStopVM's poll returns on its very first check instead of
// waiting out the real 45s grace timeout.
func TestVMStop_CallsMutagenStopPhaseBeforeGracefulStop(t *testing.T) {
	orig := mutagenStopPhaseFn
	defer func() { mutagenStopPhaseFn = orig }()

	t.Setenv("HOME", t.TempDir())
	repoRoot := t.TempDir()
	logPath := filepath.Join(repoRoot, "order.log")

	binPath := filepath.Join(repoRoot, "tart-fake")
	script := fmt.Sprintf(`#!/bin/sh
echo "$*" >> %q
case "$1" in
  list) echo '[]' ;;
esac
exit 0
`, logPath)
	require.NoError(t, os.WriteFile(binPath, []byte(script), 0o755))
	tr := tart.New()
	tr.Path = binPath

	var stopArgs []string
	mutagenStopPhaseFn = func(cfg identity.Config, projectID string) error {
		fh, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		require.NoError(t, err)
		defer fh.Close()
		_, err = fmt.Fprintln(fh, "MUTAGEN-STOP "+projectID)
		require.NoError(t, err)
		stopArgs = append(stopArgs, projectID)
		return nil
	}

	ironProxyState.put("proj-stop", projectInfo{})
	t.Cleanup(func() { ironProxyState.del("proj-stop") })

	server := NewServer(identity.Prod.SocketPath(), Build{})
	locks := NewProjectLocks()
	sup := supervisor.New(t.TempDir())
	cache := NewStateCache()
	cache.SetVMState("proj-stop", VMRunning)
	cache.SetIronProxyHealth("proj-stop", ProxyHealth{Status: ProxyOK})
	RegisterVMHandlers(server, identity.Prod, sup, tr, 0, locks, nil, nil, cache)

	body, err := json.Marshal(VMStopRequest{Name: "proj-stop"})
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	server.mux.ServeHTTP(rec, httptest.NewRequest("POST", "/vm/stop", bytes.NewReader(body)))
	require.Equal(t, http.StatusNoContent, rec.Code, "body=%s", rec.Body.String())

	row, ok := cache.ProjectRow("proj-stop")
	require.True(t, ok, "a non-destroy stop must keep the project's cache row")
	assert.Equal(t, VMStopped, row.VMState, "cache must reflect the VM as stopped")
	assert.Equal(t, ProxyMissing, row.IronProxyHealth.Status, "cache must reflect iron-proxy as torn down")

	assert.Equal(t, []string{"proj-stop"}, stopArgs, "StopPhase must be called for the right projectID")

	logBytes, err := os.ReadFile(logPath)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(logBytes)), "\n")
	stopIdx, listIdx := -1, -1
	for i, line := range lines {
		switch {
		case strings.Contains(line, "MUTAGEN-STOP"):
			stopIdx = i
		case listIdx == -1 && strings.HasPrefix(line, "list"):
			listIdx = i
		}
	}
	require.GreaterOrEqual(t, stopIdx, 0, "mutagen stop marker must be present")
	require.GreaterOrEqual(t, listIdx, 0, "gracefulStopVM's tart list call must be present")
	assert.Less(t, stopIdx, listIdx,
		"mutagen sessions must be flushed+paused BEFORE the VM's guest is powered off")
}

// TestVMCrashCallback_WritesVMStoppedToCache pins the onUnexpectedExit
// hook /vm/start wires into sup.Spawn for the VM's tart-run process
// (see vmCrashCallback + supervisor.Spawn's OnUnexpectedExit mechanism,
// exercised end-to-end at the supervisor layer by
// TestSpawn_OnUnexpectedExitFiresOnCrash). Confirms the closure itself
// writes the right project's VMState to stopped, and is nil-safe.
func TestVMCrashCallback_WritesVMStoppedToCache(t *testing.T) {
	cache := NewStateCache()
	cache.SetVMState("proj", VMRunning)

	vmCrashCallback(cache, "proj")()

	row, ok := cache.ProjectRow("proj")
	require.True(t, ok)
	assert.Equal(t, VMStopped, row.VMState)

	assert.NotPanics(t, func() { vmCrashCallback(nil, "proj")() }, "nil cache must be a safe no-op")
}

// TestVMStop_Destroy_RemovesCacheRow verifies /vm/stop's teardown path
// (Destroy=true) removes the project's cache row entirely, rather than
// just marking the VM stopped — RemoveProject, not SetVMState.
func TestVMStop_Destroy_RemovesCacheRow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repoRoot := t.TempDir()

	binPath := filepath.Join(repoRoot, "tart-fake")
	script := "#!/bin/sh\ncase \"$1\" in\n  list) echo '[]' ;;\nesac\nexit 0\n"
	require.NoError(t, os.WriteFile(binPath, []byte(script), 0o755))
	tr := tart.New()
	tr.Path = binPath

	ironProxyState.put("proj-destroy", projectInfo{})
	t.Cleanup(func() { ironProxyState.del("proj-destroy") })

	server := NewServer(identity.Prod.SocketPath(), Build{})
	locks := NewProjectLocks()
	sup := supervisor.New(t.TempDir())
	cache := NewStateCache()
	cache.SetVMState("proj-destroy", VMRunning)
	RegisterVMHandlers(server, identity.Prod, sup, tr, 0, locks, nil, nil, cache)

	body, err := json.Marshal(VMStopRequest{Name: "proj-destroy", Destroy: true})
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	server.mux.ServeHTTP(rec, httptest.NewRequest("POST", "/vm/stop", bytes.NewReader(body)))
	require.Equal(t, http.StatusNoContent, rec.Code, "body=%s", rec.Body.String())

	_, ok := cache.ProjectRow("proj-destroy")
	assert.False(t, ok, "a destroy stop must remove the project's cache row entirely")
}

// TestEndpointFrom_MapsAllFieldsToLoopback verifies endpointFrom — the
// single builder behind every setPolicy push — wires each projectInfo port
// to its own 127.0.0.1:<port> field on the returned Endpoint, with no
// cross-field swaps (e.g. HTTPS getting the DNS port).
func TestEndpointFrom_MapsAllFieldsToLoopback(t *testing.T) {
	info := projectInfo{
		HTTPPort:       5001,
		HTTPSPort:      5002,
		DNSPort:        5003,
		GuestHTTPPort:  5005,
		GuestHTTPSPort: 5006,
		PopPort:        5007,
	}
	const ntpPort = 5004

	ep := endpointFrom(info, ntpPort)

	assert.Equal(t, "127.0.0.1:5001", ep.HTTP)
	assert.Equal(t, "127.0.0.1:5002", ep.HTTPS)
	assert.Equal(t, "127.0.0.1:5003", ep.DNS)
	assert.Equal(t, "127.0.0.1:5004", ep.NTP)
	assert.Equal(t, "127.0.0.1:5005", ep.GuestHTTP)
	assert.Equal(t, "127.0.0.1:5006", ep.GuestHTTPS)
	assert.Equal(t, "127.0.0.1:5007", ep.Pop)
}

// TestVMStart_RegistersReservedHealthRoute pins that after a successful
// /vm/start, the routes table has an entry for _devm.<project>.test
// pointing at 127.0.0.1:gdevmServePort inside the guest — the route the
// Mac watchdog (Task 7) probes to reach gdevm-serve's /v1/health. This
// drives /vm/start's handler end to end: fake tart (VM reported
// pre-existing so Clone is skipped, `tart exec ... true` satisfies
// waitVMExecReady), a fake softnet control-socket listener acking
// setExposeMap, and a stubbed ironProxySpawn so no real iron-proxy
// process is started.
func TestVMStart_RegistersReservedHealthRoute(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const project = "proj-route-health"

	origMutagenStopPhaseFn := mutagenStopPhaseFn
	mutagenStopPhaseFn = func(identity.Config, string) error { return nil }
	t.Cleanup(func() { mutagenStopPhaseFn = origMutagenStopPhaseFn })

	origSpawn := ironProxySpawn
	ironProxySpawn = func(_ context.Context, _ *supervisor.Supervisor, _ supervisor.Key, _ *exec.Cmd, _ func(), _ ...io.Writer) error {
		return nil
	}
	t.Cleanup(func() { ironProxySpawn = origSpawn })
	t.Cleanup(func() { policyAuthority.StopServing(project) })

	// Fake `tart` on $PATH: satisfies both tart.Tart's t.Path-based
	// calls (List/Run) and waitVMExecReady's literal exec.Command("tart",
	// "exec", ...), which shells out by bare name rather than through
	// the *tart.Tart wrapper.
	binDir := t.TempDir()
	tartScript := fmt.Sprintf(`#!/bin/sh
case "$1" in
  list) echo '[{"Name":%q,"State":"stopped"}]' ;;
  exec) exit 0 ;;
  run) sleep 30 ;;
esac
exit 0
`, project)
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "tart"), []byte(tartScript), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	tr := tart.New()
	tr.Path = "tart"

	logDir := t.TempDir()
	sup := supervisor.New(logDir)
	t.Cleanup(func() {
		_ = sup.Stop(context.Background(), supervisor.Key{ProjectID: project, Role: supervisor.RoleVM})
	})

	t.Cleanup(func() {
		ironProxyState.del(project)
		softnetState.del(project)
		exposeClaims.release(project)
	})

	// Fake softnet control socket: acks setExposeMap (fatal if unacked)
	// and silently accepts setTestHosts (fire-and-forget, non-fatal).
	// The setExposeMap line is captured so the test can assert its
	// contents below — this is the wire evidence that gdevmServePort
	// (8940) was auto-exposed alongside :22, not just that some line
	// arrived.
	require.NoError(t, ensureSoftnetSockDir(softnetSockDir()))
	sockPath := SoftnetControlSock(identity.Prod, project)
	_ = os.Remove(sockPath)
	ln, err := net.Listen("unix", sockPath)
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })
	exposeLines := make(chan string, 1)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				line, err := bufio.NewReader(c).ReadString('\n')
				if err != nil {
					return
				}
				if strings.Contains(line, `"op":"setExposeMap"`) {
					select {
					case exposeLines <- line:
					default:
					}
					_, _ = c.Write([]byte(`{"ok":true}` + "\n"))
				}
			}(c)
		}
	}()

	macCwd := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(macCwd, "devm.yaml"),
		[]byte("project:\n  name: "+project+"\n"), 0o644))

	server := NewServer(identity.Prod.SocketPath(), Build{})
	locks := NewProjectLocks()
	cache := NewStateCache()
	routes := NewRoutes(identity.Prod.TLD)
	RegisterVMHandlers(server, identity.Prod, sup, tr, 0, locks, nil, routes, cache)

	body, err := json.Marshal(VMStartRequest{Name: project, MacCwd: macCwd, Cfg: schema.Config{}})
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	server.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/vm/start", bytes.NewReader(body)))
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	var startResp VMStartResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &startResp))
	require.NotEmpty(t, startResp.ProjectIP, "vm/start response must carry the allocated projectIP")

	route, ok := routes.Lookup("_devm."+project+".test", project)
	require.True(t, ok, "reserved health route must be registered after /vm/start")
	assert.Equal(t, gdevmServePort, route.BackendPort)
	// BackendHost must be the guest-reachable projectIP, not the Mac's
	// own loopback — 127.0.0.1 on the Mac daemon's side is the Mac
	// itself, not softnet's forward-to-guest alias, so a route baked
	// with it dials nothing.
	assert.Equal(t, startResp.ProjectIP, route.BackendHost)
	assert.NotEqual(t, "127.0.0.1", route.BackendHost)
	assert.Equal(t, project, route.Project)
	assert.Equal(t, ModeVM, route.Mode)

	select {
	case line := <-exposeLines:
		// gdevm-serve's health port must be auto-exposed on projectIP
		// alongside :22 — this is what makes the reserved route's
		// projectIP:8940 dial above actually reach the guest.
		assert.Contains(t, line, fmt.Sprintf(`"guest_port":%d`, gdevmServePort))
		assert.Contains(t, line, fmt.Sprintf(`"bind_ip":%q`, startResp.ProjectIP))
	default:
		t.Fatal("no setExposeMap line captured from fake softnet control socket")
	}
}

// TestVMStart_SetsProxyListenerHealthOptimistically pins Task 8's review
// follow-up: ProjectRow.ProxyListenerHealth is otherwise written from
// exactly one call site, watchdog_check_proxy_listener.go's 60-second
// tick, which left it at its Go zero value (false) for up to a minute
// after every cold /vm/start — showing "proxy: UNHEALTHY" on the most
// common path even though the listener bind just succeeded. /vm/start
// must seed the cache optimistically, the same way it already does for
// cache.SetIronProxyHealth right next to it, so devm status is accurate
// immediately after start rather than only after the next watchdog tick.
func TestVMStart_SetsProxyListenerHealthOptimistically(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const project = "proj-listener-health"

	origMutagenStopPhaseFn := mutagenStopPhaseFn
	mutagenStopPhaseFn = func(identity.Config, string) error { return nil }
	t.Cleanup(func() { mutagenStopPhaseFn = origMutagenStopPhaseFn })

	origSpawn := ironProxySpawn
	ironProxySpawn = func(_ context.Context, _ *supervisor.Supervisor, _ supervisor.Key, _ *exec.Cmd, _ func(), _ ...io.Writer) error {
		return nil
	}
	t.Cleanup(func() { ironProxySpawn = origSpawn })
	t.Cleanup(func() { policyAuthority.StopServing(project) })

	// Fake `tart` on $PATH: satisfies both tart.Tart's t.Path-based
	// calls (List/Run) and waitVMExecReady's literal exec.Command("tart",
	// "exec", ...), which shells out by bare name rather than through
	// the *tart.Tart wrapper.
	binDir := t.TempDir()
	tartScript := fmt.Sprintf(`#!/bin/sh
case "$1" in
  list) echo '[{"Name":%q,"State":"stopped"}]' ;;
  exec) exit 0 ;;
  run) sleep 30 ;;
esac
exit 0
`, project)
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "tart"), []byte(tartScript), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	tr := tart.New()
	tr.Path = "tart"

	logDir := t.TempDir()
	sup := supervisor.New(logDir)
	t.Cleanup(func() {
		_ = sup.Stop(context.Background(), supervisor.Key{ProjectID: project, Role: supervisor.RoleVM})
	})

	t.Cleanup(func() {
		ironProxyState.del(project)
		softnetState.del(project)
		exposeClaims.release(project)
	})

	// Fake softnet control socket: acks setExposeMap (fatal if unacked)
	// and silently accepts setTestHosts (fire-and-forget, non-fatal).
	require.NoError(t, ensureSoftnetSockDir(softnetSockDir()))
	sockPath := SoftnetControlSock(identity.Prod, project)
	_ = os.Remove(sockPath)
	ln, err := net.Listen("unix", sockPath)
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				line, err := bufio.NewReader(c).ReadString('\n')
				if err != nil {
					return
				}
				if strings.Contains(line, `"op":"setExposeMap"`) {
					_, _ = c.Write([]byte(`{"ok":true}` + "\n"))
				}
			}(c)
		}
	}()

	macCwd := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(macCwd, "devm.yaml"),
		[]byte("project:\n  name: "+project+"\n"), 0o644))

	server := NewServer(identity.Prod.SocketPath(), Build{})
	locks := NewProjectLocks()
	cache := NewStateCache()
	routes := NewRoutes(identity.Prod.TLD)
	RegisterVMHandlers(server, identity.Prod, sup, tr, 0, locks, nil, routes, cache)

	body, err := json.Marshal(VMStartRequest{Name: project, MacCwd: macCwd, Cfg: schema.Config{}})
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	server.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/vm/start", bytes.NewReader(body)))
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	row, ok := cache.ProjectRow(project)
	require.True(t, ok, "cache row must exist after /vm/start")
	assert.True(t, row.ProxyListenerHealth,
		"ProxyListenerHealth must be seeded true immediately after a successful /vm/start, not left at its zero value until the next watchdog tick")
}
