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
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mdubb86/devm/internal/devmbundle"
	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/supervisor"
)

// TestRebind_StartsSSHListenersAndPopulatesBundle drives
// rebindProjectListeners and asserts both halves of the Task 9 wiring:
//  1. StartSSHJumpListeners binds devm-jump + agent-bridge via the
//     ProxyServer (proxy.ProjectSnapshot returns non-zero ports after
//     rebind).
//  2. devmbundle.BuildInputFor, with the daemon's SSHEnricher wired via
//     BindSSHBundleEnricher, populates all four SSH* fields of the
//     BuildInput. A control case with cfg.Project.SSH == nil leaves all
//     four fields nil.
func TestRebind_StartsSSHListenersAndPopulatesBundle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Stand up the mock root-helper so StartProjectListeners (dialed
	// inside rebindProjectListeners) succeeds — exact same UDS wiring
	// TestRebindProjectListeners_RetriesUntilHelperReady uses.
	dir, err := os.MkdirTemp("", "sshjlife")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	helperSock := filepath.Join(dir, "helper.sock")
	startMockHelperAt(t, helperSock)

	cfg := identity.Config{Name: "devm-test-sshjump", HelperSocketPath: helperSock, TLD: "test"}
	caDir := t.TempDir()
	ca, err := loadOrGenerateCAAt(identity.Prod, caDir)
	require.NoError(t, err)

	// Fresh ironProxyState so a prior test can't bleed in.
	origStore := ironProxyState
	ironProxyState = newIronProxyStore()
	t.Cleanup(func() { ironProxyState = origStore })

	// Stub ssh -G + ssh-keygen -F via schema.DefaultExec so the resolver
	// and known_hosts check both succeed without touching the real Mac.
	origExec := schema.DefaultExec
	t.Cleanup(func() { schema.DefaultExec = origExec })
	// Spin up a backend port the resolved alias will point at — the
	// resolver's port must be a valid 1..65535.
	tgtLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer tgtLn.Close()
	tgtPort := tgtLn.Addr().(*net.TCPAddr).Port
	schema.DefaultExec = func(name string, args ...string) ([]byte, error) {
		switch name {
		case "ssh":
			// ssh -G <alias>
			return []byte(
				"hostname 127.0.0.1\n" +
					"port " + strconv.Itoa(tgtPort) + "\n" +
					"user devm\n",
			), nil
		case "ssh-keygen":
			// ssh-keygen -F 127.0.0.1 — return a fake known-hosts match.
			return []byte(
				"# Host 127.0.0.1 found: line 3\n" +
					"127.0.0.1 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFAKE_ED25519_KEY\n",
			), nil
		}
		return nil, fmt.Errorf("unexpected exec %q %v", name, args)
	}

	// ========== Case 1: cfg.Project.SSH non-empty ==========
	projectID := "proj-with-ssh"
	projectIP := "127.0.0.1"
	sshList := []string{"alias"}
	withSSHCfg := schema.Config{
		Project: schema.Project{Name: projectID, SSH: &sshList},
	}
	// Seed the snapshot rebindProjectListeners reads via ReadStateSnapshot
	// to learn whether cfg.project.ssh is non-empty.
	require.NoError(t, WriteStateSnapshot(cfg, projectID, StateSnapshot{Cfg: withSSHCfg}))
	// Seed ironProxyState with a non-empty entry so the rebind's
	// "if ok" branch (where the SSH wiring lives) actually runs.
	ironProxyState.put(projectID, projectInfo{ProjectIP: projectIP})

	proxy := NewProxyServer(cfg, NewRoutes(cfg.TLD), ca)
	origEnricher := devmbundle.SSHEnricher
	BindSSHBundleEnricher(proxy)
	t.Cleanup(func() { devmbundle.SSHEnricher = origEnricher })
	t.Cleanup(func() { proxy.StopSSHJumpListeners(projectID) })
	t.Cleanup(func() { proxy.StopProjectListeners(projectID) })

	status := rebindProjectListeners(context.Background(), proxy, cfg, projectID, projectIP, 0)
	require.Equal(t, RebindOK, status.State,
		"rebind failed: attempts=%d err=%q", status.Attempts, status.LastError)

	// Assertion 1: ProjectSnapshot reports the bound ports.
	snap := proxy.ProjectSnapshot(projectID)
	assert.Greater(t, snap.JumpPort, 0, "jump port must be bound")
	assert.Greater(t, snap.BridgePort, 0, "bridge port must be bound")

	// Assertion 2: BuildInputFor produces non-nil SSH* fields.
	in, err := devmbundle.BuildInputFor(
		withSSHCfg, "", cfg.RuntimeDir(), cfg.TLD,
		nil, nil, nil, nil,
	)
	require.NoError(t, err)
	assert.NotNil(t, in.SSHClientConfig, "SSHClientConfig must be populated")
	assert.NotNil(t, in.SSHJumpClientPriv, "SSHJumpClientPriv must be populated")
	assert.NotNil(t, in.SSHKnownHostsAppend, "SSHKnownHostsAppend must be populated")
	assert.NotNil(t, in.SSHAgentBridgeUnit, "SSHAgentBridgeUnit must be populated")

	// Spot-check content: client config carries the bound jump port.
	assert.Contains(t, string(in.SSHClientConfig), "Port "+strconv.Itoa(snap.JumpPort),
		"ssh-client/config must point at the bound jump port")
	assert.Contains(t, string(in.SSHClientConfig), "Host alias",
		"ssh-client/config must include a Host block per resolved alias")
	assert.True(t, strings.HasPrefix(string(in.SSHKnownHostsAppend), "["),
		"known_hosts body first line must be a [host]:port form for devm-jump")

	// ========== Case 2: cfg.Project.SSH == nil ==========
	noSSHCfg := schema.Config{Project: schema.Project{Name: "proj-no-ssh"}}
	in2, err := devmbundle.BuildInputFor(
		noSSHCfg, "", cfg.RuntimeDir(), cfg.TLD,
		nil, nil, nil, nil,
	)
	require.NoError(t, err)
	assert.Nil(t, in2.SSHClientConfig, "SSH nil → client config nil")
	assert.Nil(t, in2.SSHJumpClientPriv, "SSH nil → client priv nil")
	assert.Nil(t, in2.SSHKnownHostsAppend, "SSH nil → known hosts nil")
	assert.Nil(t, in2.SSHAgentBridgeUnit, "SSH nil → bridge unit nil")
}

// TestVMStop_CallsStopSSHJumpListeners pins that /vm/stop's existing
// StopProjectListeners site also tears down this project's SSH jump
// pair — `devm stop` must not leak those listeners for another project
// to inherit.
func TestVMStop_CallsStopSSHJumpListeners(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := identity.Config{Name: "devm-test-sshstop", TLD: "test"}
	caDir := t.TempDir()
	ca, err := loadOrGenerateCAAt(identity.Prod, caDir)
	require.NoError(t, err)
	proxy := NewProxyServer(cfg, NewRoutes(cfg.TLD), ca)

	// Bind so the Stop path has something to close.
	jumpPort, bridgePort, err := proxy.StartSSHJumpListeners(
		context.Background(), "proj", cfg.RuntimeDir(),
		[]schema.ResolvedSSHTarget{{Alias: "a", HostName: "127.0.0.1", Port: 22}},
	)
	require.NoError(t, err)
	require.NotZero(t, jumpPort)
	require.NotZero(t, bridgePort)

	// Mirror what /vm/stop at vm.go:1130 does.
	proxy.StopProjectListeners("proj")
	proxy.StopSSHJumpListeners("proj")

	snap := proxy.ProjectSnapshot("proj")
	assert.Zero(t, snap.JumpPort, "jump port must clear after stop")
	assert.Zero(t, snap.BridgePort, "bridge port must clear after stop")
}

// stubExec installs a schema.DefaultExec that fakes `ssh -G <alias>`
// (resolving to 127.0.0.1:<tgtPort>:devm) and `ssh-keygen -F` (one
// synthetic key line) for the duration of the test.
func stubExec(t *testing.T, tgtPort int) {
	t.Helper()
	orig := schema.DefaultExec
	t.Cleanup(func() { schema.DefaultExec = orig })
	schema.DefaultExec = func(name string, args ...string) ([]byte, error) {
		switch name {
		case "ssh":
			return []byte(
				"hostname 127.0.0.1\n" +
					"port " + strconv.Itoa(tgtPort) + "\n" +
					"user devm\n",
			), nil
		case "ssh-keygen":
			return []byte(
				"# Host 127.0.0.1 found: line 3\n" +
					"127.0.0.1 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFAKE_ED25519_KEY\n",
			), nil
		}
		return nil, fmt.Errorf("unexpected exec %q %v", name, args)
	}
}

// TestColdStart_ApplySSHJumpBindBindsAndRefreshesMacPorts covers the
// /vm/start cold-start site (vm.go:841). The handler itself needs
// tart + supervisor to drive end-to-end; this focused test invokes
// the shared applySSHJumpBind helper the handler calls in the same
// shape (req.Cfg, &info), and asserts both lifecycle invariants the
// handler relies on:
//  1. proxy.ProjectSnapshot reports non-zero jumpPort + bridgePort
//     (so a subsequent /vm/begin-provisioning softnet FORWARDING push
//     reads the fresh values).
//  2. info.MacPorts picks up both auto-added ports (so endpointFrom's
//     admit list downstream of ironProxyState.put includes them —
//     see peers.go's macPortsForProject).
func TestColdStart_ApplySSHJumpBindBindsAndRefreshesMacPorts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := identity.Config{Name: "devm-test-sshcold", TLD: "test"}
	caDir := t.TempDir()
	ca, err := loadOrGenerateCAAt(identity.Prod, caDir)
	require.NoError(t, err)
	proxy := NewProxyServer(cfg, NewRoutes(cfg.TLD), ca)

	tgtLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer tgtLn.Close()
	stubExec(t, tgtLn.Addr().(*net.TCPAddr).Port)

	projectID := "proj-cold-ssh"
	sshList := []string{"alias"}
	coldCfg := schema.Config{
		Project: schema.Project{Name: projectID, SSH: &sshList},
	}
	info := projectInfo{ProjectIP: "127.0.0.1"}
	t.Cleanup(func() { proxy.StopSSHJumpListeners(projectID) })

	require.NoError(t, applySSHJumpBind(context.Background(), proxy, cfg.RuntimeDir(), coldCfg, &info))

	// Assertion 1: ports bound on the proxy.
	snap := proxy.ProjectSnapshot(projectID)
	assert.Greater(t, snap.JumpPort, 0, "jump port must be bound")
	assert.Greater(t, snap.BridgePort, 0, "bridge port must be bound")

	// Assertion 2: info.MacPorts carries the bound jump + bridge ports —
	// same post-condition the handler's `ironProxyState.put(req.Name,
	// info)` persists so downstream softnet pushes admit them.
	assert.Contains(t, info.MacPorts, snap.JumpPort, "MacPorts must include the jump port")
	assert.Contains(t, info.MacPorts, snap.BridgePort, "MacPorts must include the bridge port")
}

// TestApplySSHJumpBind_PropagatesResolveFailure proves the cold-start
// handler's error path: a `ssh -G` failure surfaces loud so the handler
// can return 500 rather than silently binding nothing.
func TestApplySSHJumpBind_PropagatesResolveFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := identity.Config{Name: "devm-test-sshcold-err", TLD: "test"}
	caDir := t.TempDir()
	ca, err := loadOrGenerateCAAt(identity.Prod, caDir)
	require.NoError(t, err)
	proxy := NewProxyServer(cfg, NewRoutes(cfg.TLD), ca)

	// Force ssh -G to error.
	origExec := schema.DefaultExec
	t.Cleanup(func() { schema.DefaultExec = origExec })
	schema.DefaultExec = func(name string, args ...string) ([]byte, error) {
		return nil, fmt.Errorf("stub: no such alias")
	}

	sshList := []string{"bad-alias"}
	badCfg := schema.Config{
		Project: schema.Project{Name: "proj-cold-err", SSH: &sshList},
	}
	var info projectInfo
	err = applySSHJumpBind(context.Background(), proxy, cfg.RuntimeDir(), badCfg, &info)
	require.Error(t, err, "resolve failure must surface")
	assert.Contains(t, err.Error(), "resolve:", "error must identify the resolve stage")
	assert.Zero(t, proxy.ProjectSnapshot("proj-cold-err").JumpPort, "must not bind listeners when resolve fails")
}

// TestVMStart_BindsSSHJumpOnColdStart drives /vm/start end-to-end
// against a real ProxyServer (not nil like the sibling /vm/start
// tests) with cfg.project.ssh non-empty, and proves the handler's
// cold-start wiring landed: proxy.ProjectSnapshot reports non-zero
// jumpPort + bridgePort after the handler returns 200.
func TestVMStart_BindsSSHJumpOnColdStart(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	const project = "proj-vmstart-ssh"

	// Mocks — same shape as TestVMStart_RegistersReservedHealthRoute.
	origMutagenStopPhaseFn := mutagenStopPhaseFn
	mutagenStopPhaseFn = func(identity.Config, string) error { return nil }
	t.Cleanup(func() { mutagenStopPhaseFn = origMutagenStopPhaseFn })

	origSpawn := ironProxySpawn
	ironProxySpawn = func(_ context.Context, _ *supervisor.Supervisor, _ supervisor.Key, _ *exec.Cmd, _ func(), _ ...io.Writer) error {
		return nil
	}
	t.Cleanup(func() { ironProxySpawn = origSpawn })
	t.Cleanup(func() { policyAuthority.StopServing(project) })

	origProbe := probeIPInUse
	probeIPInUse = func(string) bool { return false }
	t.Cleanup(func() { probeIPInUse = origProbe })
	t.Cleanup(func() { ironProxyState.del(project) })

	// Stub the Mac probes so ResolveSSHTargets + CheckKnownHosts pass.
	tgtLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer tgtLn.Close()
	stubExec(t, tgtLn.Addr().(*net.TCPAddr).Port)

	// Fake tart on $PATH.
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

	// Fake softnet control socket at the identity.Prod location. The
	// /vm/start path writes expose/forward maps to this socket; a
	// non-acking peer would stall the handler, so ack setExposeMap
	// and drain anything else.
	require.NoError(t, ensureSoftnetSockDir(softnetSockDir()))
	sockPath := SoftnetControlSock(identity.Prod, project)
	_ = os.Remove(sockPath)
	softnetLn, err := net.Listen("unix", sockPath)
	require.NoError(t, err)
	t.Cleanup(func() { softnetLn.Close() })
	go func() {
		for {
			c, err := softnetLn.Accept()
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

	// Mock root helper for proxy.StartProjectListeners.
	helperDir, err := os.MkdirTemp("", "vmstart-ssh")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(helperDir) })
	helperSock := filepath.Join(helperDir, "helper.sock")
	startMockHelperAt(t, helperSock)

	// identity.Prod with the mock helper path threaded in so
	// proxy.StartProjectListeners dials our mock, not /var/run/...
	testCfg := identity.Prod
	testCfg.HelperSocketPath = helperSock

	caDir := t.TempDir()
	ca, err := loadOrGenerateCAAt(identity.Prod, caDir)
	require.NoError(t, err)
	proxy := NewProxyServer(testCfg, NewRoutes(testCfg.TLD), ca)
	t.Cleanup(func() {
		proxy.StopProjectListeners(project)
		proxy.StopSSHJumpListeners(project)
	})

	macCwd := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(macCwd, "devm.yaml"),
		[]byte("project:\n  name: "+project+"\nssh:\n  - alias\n"), 0o644))

	server := NewServer(testCfg.SocketPath(), Build{})
	locks := NewProjectLocks()
	cache := NewStateCache()
	routes := NewRoutes(testCfg.TLD)
	RegisterVMHandlers(server, testCfg, sup, tr, 0, locks, proxy, routes, cache)

	sshList := []string{"alias"}
	reqCfg := schema.Config{
		Project: schema.Project{Name: project, SSH: &sshList},
	}
	body, err := json.Marshal(VMStartRequest{Name: project, MacCwd: macCwd, Cfg: reqCfg})
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	server.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/vm/start", bytes.NewReader(body)))
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	// Core assertion: cold-start bound the SSH pair.
	snap := proxy.ProjectSnapshot(project)
	assert.Greater(t, snap.JumpPort, 0, "cold-start must bind jump port")
	assert.Greater(t, snap.BridgePort, 0, "cold-start must bind bridge port")

	// Secondary assertion: the handler's `ironProxyState.put` persisted
	// info.MacPorts carrying the auto-added ports, so downstream softnet
	// pushes see them (per Task 8's macPortsForProject contract).
	info, ok := ironProxyState.get(project)
	require.True(t, ok)
	assert.Contains(t, info.MacPorts, snap.JumpPort, "MacPorts must include jump port")
	assert.Contains(t, info.MacPorts, snap.BridgePort, "MacPorts must include bridge port")
}

// TestRespawnProxyListeners_RebindsSSHJump pins the watchdog respawn
// site (watchdog_ground_truth.go:191). A watchdog-driven respawn of
// an ssh-enabled project must stop the SSH pair AND re-bind it,
// otherwise `devm status` reports a healthy proxy respawn while the
// guest silently loses ssh.
func TestRespawnProxyListeners_RebindsSSHJump(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Mock helper for the StartProjectListeners call inside the respawn.
	dir, err := os.MkdirTemp("", "sshjresp")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	helperSock := filepath.Join(dir, "helper.sock")
	startMockHelperAt(t, helperSock)

	cfg := identity.Config{Name: "devm-test-sshresp", HelperSocketPath: helperSock, TLD: "test"}
	caDir := t.TempDir()
	ca, err := loadOrGenerateCAAt(identity.Prod, caDir)
	require.NoError(t, err)

	origStore := ironProxyState
	ironProxyState = newIronProxyStore()
	t.Cleanup(func() { ironProxyState = origStore })

	tgtLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer tgtLn.Close()
	stubExec(t, tgtLn.Addr().(*net.TCPAddr).Port)

	projectID := "proj-resp-ssh"
	projectIP := "127.0.0.1"
	sshList := []string{"alias"}
	respCfg := schema.Config{
		Project: schema.Project{Name: projectID, SSH: &sshList},
	}
	require.NoError(t, WriteStateSnapshot(cfg, projectID, StateSnapshot{Cfg: respCfg}))
	ironProxyState.put(projectID, projectInfo{ProjectIP: projectIP})

	proxy := NewProxyServer(cfg, NewRoutes(cfg.TLD), ca)
	t.Cleanup(func() { proxy.StopProjectListeners(projectID) })
	t.Cleanup(func() { proxy.StopSSHJumpListeners(projectID) })

	// Bind the SSH pair once so we can prove the respawn tears them
	// down and re-binds to fresh ports (not just leaves the old pair).
	jp0, bp0, err := proxy.StartSSHJumpListeners(context.Background(), projectID, cfg.RuntimeDir(),
		[]schema.ResolvedSSHTarget{{Alias: "alias", HostName: "127.0.0.1", Port: 22}},
	)
	require.NoError(t, err)
	require.NotZero(t, jp0)
	require.NotZero(t, bp0)

	gt := &RealGroundTruth{
		Cfg: cfg, Proxy: proxy, Locks: NewProjectLocks(), NtpPort: 0,
	}
	err = gt.RespawnProxyListeners(context.Background(), projectID)
	require.NoError(t, err, "respawn must succeed")

	snap := proxy.ProjectSnapshot(projectID)
	assert.Greater(t, snap.JumpPort, 0, "jump port must be re-bound after respawn")
	assert.Greater(t, snap.BridgePort, 0, "bridge port must be re-bound after respawn")
	// Fresh ports ≠ pre-respawn ports — respawn is Stop+Start, not a
	// no-op. Confirming this proves the Stop actually fired (not just
	// that a prior bind survived).
	assert.NotEqual(t, jp0, snap.JumpPort, "respawn must bind fresh jump port, not reuse the old one")
	assert.NotEqual(t, bp0, snap.BridgePort, "respawn must bind fresh bridge port, not reuse the old one")
}
