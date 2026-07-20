package serviceapi

import (
	"bufio"
	"context"
	"net"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mdubb86/devm/internal/identity"
)

// TestDiscoverSoftnet_RebuildsStateForRehydratedProjects covers the
// daemon-restart adopt path: discoverSoftnet runs after AdoptIronProxies
// has rehydrated ironProxyState, and must rebuild softnetState (in-memory
// only, so empty after a restart) for every project it finds there — the
// running VM's softnet child is still alive (setsid'd, survives daemon
// death) and needs its control socket re-registered so
// /vm/open-egress and /vm/apply-egress-enforcement work again without a
// fresh /vm/start. It should also best-effort re-push ENFORCED so a
// softnet that itself restarted (and came back up LOCKED) gets
// reconciled.
//
// Uses os.MkdirTemp (short prefix) rather than t.TempDir: a unix socket
// path is capped at ~104 bytes on macOS, and t.TempDir embeds the full
// test name in the path.
func TestDiscoverSoftnet_RebuildsStateForRehydratedProjects(t *testing.T) {
	const projectID = "discover-proj"
	dir, err := os.MkdirTemp("", "sn-discover")
	require.NoError(t, err)
	defer os.RemoveAll(dir)
	t.Setenv("DEVM_RUNTIME_DIR", dir)

	ironProxyState.put(projectID, projectInfo{
		HTTPPort: 8080, HTTPSPort: 8443, DNSPort: 8053,
	})
	t.Cleanup(func() { ironProxyState.del(projectID) })
	t.Cleanup(func() { softnetState.del(projectID) })

	// Stand in for the surviving softnet child so the best-effort
	// setPolicy push has somewhere to land.
	sock := SoftnetControlSock(identity.Prod, projectID)
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	defer ln.Close()
	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		line, _ := r.ReadString('\n')
		got <- line
	}()

	discoverSoftnet(context.Background(), identity.Prod, 51234)

	assert.Equal(t, sock, softnetState.get(projectID),
		"discoverSoftnet must re-put the deterministic control sock for every rehydrated project")

	line := <-got
	assert.Contains(t, line, `"op":"setPolicy"`)
	assert.Contains(t, line, `"policy":"ENFORCED"`)
	assert.Contains(t, line, `"http":"127.0.0.1:8080"`)
	assert.Contains(t, line, `"ntp":"127.0.0.1:51234"`)
}

// TestDiscoverSoftnet_NoRehydratedProjects_NoOp verifies discoverSoftnet
// is a harmless no-op when ironProxyState has nothing to walk — a clean
// daemon start with no VMs running, or one where AdoptIronProxies found
// no surviving iron-proxies.
func TestDiscoverSoftnet_NoRehydratedProjects_NoOp(t *testing.T) {
	dir, err := os.MkdirTemp("", "sn-discover-empty")
	require.NoError(t, err)
	defer os.RemoveAll(dir)
	t.Setenv("DEVM_RUNTIME_DIR", dir)

	assert.NotPanics(t, func() {
		discoverSoftnet(context.Background(), identity.Prod, 51234)
	})
}
