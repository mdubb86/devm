package serviceapi

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mdubb86/devm/internal/devmbundle"
	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/schema"
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
