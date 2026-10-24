package serviceapi

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPeersIndex_Basic(t *testing.T) {
	p := newPeersIndex()
	p.setPeers("A", []string{"B", "C"})
	p.setPeers("D", []string{"B"})

	got := p.dependentsOf("B")
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"A", "D"}) {
		t.Fatalf("dependentsOf B: %v", got)
	}

	p.remove("A")
	got = p.dependentsOf("B")
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"D"}) {
		t.Fatalf("post-remove: %v", got)
	}

	// C's inverse edge from A also vanished.
	if deps := p.dependentsOf("C"); len(deps) != 0 {
		t.Fatalf("dependentsOf C after remove A: %v", deps)
	}
}

func TestPeersIndex_SetPeersReplacesOldEdges(t *testing.T) {
	p := newPeersIndex()
	p.setPeers("A", []string{"B", "C"})
	// Replace A's declaration with just C — B's inverse edge from A
	// must disappear.
	p.setPeers("A", []string{"C"})

	if deps := p.dependentsOf("B"); len(deps) != 0 {
		t.Fatalf("dependentsOf B after re-setPeers: %v", deps)
	}
	got := p.dependentsOf("C")
	if !reflect.DeepEqual(got, []string{"A"}) {
		t.Fatalf("dependentsOf C: %v", got)
	}
}

// TestPeersIndex_RemoveKeepsDepsToMe pins the documented asymmetry:
// remove(projectID) drops projectID's outgoing peer declarations
// (peersOf) but leaves the inverse edges where projectID appears as
// someone else's peer. Dependents still need to learn when projectID
// comes back up.
func TestPeersIndex_RemoveKeepsDepsToMe(t *testing.T) {
	p := newPeersIndex()
	p.setPeers("A", []string{"B"})

	// B is torn down; A's listing of B must survive so A gets a fresh
	// setPolicy when B rebinds later.
	p.remove("B")

	got := p.dependentsOf("B")
	if !reflect.DeepEqual(got, []string{"A"}) {
		t.Fatalf("dependentsOf B after remove(B): %v (want [A])", got)
	}
}

func TestPeersIndex_HairpinsFor_LivePorts(t *testing.T) {
	p := newPeersIndex()
	p.setPeers("A", []string{"B"})

	s := newIronProxyStore()
	s.put("B", projectInfo{GuestHTTPSPort: 61234, GuestHTTPPort: 61235})

	hs := p.peerHairpinsFor("A", s, "test")
	if hs["B.test"] != "127.0.0.1:61234" {
		t.Fatalf("https: %v", hs)
	}
	hh := p.peerHairpinsHTTPFor("A", s, "test")
	if hh["B.test"] != "127.0.0.1:61235" {
		t.Fatalf("http: %v", hh)
	}
}

// TestPeersIndex_HairpinsFor_TLD proves the tld is threaded through:
// the e2e daemon's TLD is "e2e.test", so the map keys must use that
// suffix, not a hardcoded ".test".
func TestPeersIndex_HairpinsFor_TLD(t *testing.T) {
	p := newPeersIndex()
	p.setPeers("A", []string{"B"})

	s := newIronProxyStore()
	s.put("B", projectInfo{GuestHTTPSPort: 61234, GuestHTTPPort: 61235})

	hs := p.peerHairpinsFor("A", s, "e2e.test")
	if hs["B.e2e.test"] != "127.0.0.1:61234" {
		t.Fatalf("https: %v", hs)
	}
	if _, ok := hs["B.test"]; ok {
		t.Fatalf("hardcoded .test suffix leaked: %v", hs)
	}
	hh := p.peerHairpinsHTTPFor("A", s, "e2e.test")
	if hh["B.e2e.test"] != "127.0.0.1:61235" {
		t.Fatalf("http: %v", hh)
	}
}

func TestPeersIndex_HairpinsFor_PeerDown_OmittedNotZero(t *testing.T) {
	p := newPeersIndex()
	p.setPeers("A", []string{"B"})

	// B not registered in projectInfoStore — simulates its VM being down.
	s := newIronProxyStore()
	if hs := p.peerHairpinsFor("A", s, "test"); len(hs) != 0 {
		t.Fatalf("peer-down must be omitted, got %v", hs)
	}
	if hh := p.peerHairpinsHTTPFor("A", s, "test"); len(hh) != 0 {
		t.Fatalf("peer-down must be omitted, got %v", hh)
	}
}

func TestPeersIndex_HairpinsFor_ZeroPort_OmittedNotZero(t *testing.T) {
	p := newPeersIndex()
	p.setPeers("A", []string{"B"})

	// B is registered but the guest-origin listener pair hasn't bound
	// yet — the SNI mux must see a missing map entry rather than a
	// 127.0.0.1:0 that dials nothing.
	s := newIronProxyStore()
	s.put("B", projectInfo{GuestHTTPSPort: 0, GuestHTTPPort: 0})
	if hs := p.peerHairpinsFor("A", s, "test"); len(hs) != 0 {
		t.Fatalf("zero HTTPS port must be omitted, got %v", hs)
	}
	if hh := p.peerHairpinsHTTPFor("A", s, "test"); len(hh) != 0 {
		t.Fatalf("zero HTTP port must be omitted, got %v", hh)
	}
}

// TestPeersIndex_HairpinsFor_MixedZeroPort covers the independent-port
// case: a peer may have bound its HTTPS listener but not its HTTP one
// (or vice versa). Each accessor must decide on its own port field.
func TestPeersIndex_HairpinsFor_MixedZeroPort(t *testing.T) {
	p := newPeersIndex()
	p.setPeers("A", []string{"B"})
	s := newIronProxyStore()

	// Only the HTTPS port is bound: HTTPS accessor includes B, HTTP omits.
	s.put("B", projectInfo{GuestHTTPSPort: 61234, GuestHTTPPort: 0})
	if hs := p.peerHairpinsFor("A", s, "test"); hs["B.test"] != "127.0.0.1:61234" {
		t.Fatalf("https: %v", hs)
	}
	if hh := p.peerHairpinsHTTPFor("A", s, "test"); len(hh) != 0 {
		t.Fatalf("http must be omitted when HTTP port is zero: %v", hh)
	}

	// Only the HTTP port is bound: HTTP includes B, HTTPS omits.
	s.put("B", projectInfo{GuestHTTPSPort: 0, GuestHTTPPort: 61235})
	if hs := p.peerHairpinsFor("A", s, "test"); len(hs) != 0 {
		t.Fatalf("https must be omitted when HTTPS port is zero: %v", hs)
	}
	if hh := p.peerHairpinsHTTPFor("A", s, "test"); hh["B.test"] != "127.0.0.1:61235" {
		t.Fatalf("http: %v", hh)
	}
}

// TestPropagatePeerRebind_PushesSetPolicyToDependents proves the fanout:
// when project B's hairpin rebinds, every project that lists B as a peer
// receives a setPolicy(FORWARDING) push on its own softnet control
// socket. The test drives propagatePeerRebind directly rather than the
// full rebindProjectListeners path — that function needs a real
// ProxyServer (CA, routes, helper socket) to exercise, and the behavior
// under test here is the fanout, not the listener bind.
func TestPropagatePeerRebind_PushesSetPolicyToDependents(t *testing.T) {
	// Save and restore package state so parallel suites don't collide.
	origIron := ironProxyState
	origSoftnet := softnetState
	origPeers := peersState
	ironProxyState = newIronProxyStore()
	softnetState = newSoftnetStore()
	peersState = newPeersIndex()
	t.Cleanup(func() {
		ironProxyState = origIron
		softnetState = origSoftnet
		peersState = origPeers
	})

	// Two dependents (A, C), one unrelated project (U), and the peer
	// whose rebind we simulate (B). A and C both list B as a peer; U
	// does not. Only A and C should receive the push.
	ironProxyState.put("A", projectInfo{HTTPPort: 11080, HTTPSPort: 11443, DNSPort: 11053})
	ironProxyState.put("C", projectInfo{HTTPPort: 12080, HTTPSPort: 12443, DNSPort: 12053})
	ironProxyState.put("U", projectInfo{HTTPPort: 13080, HTTPSPort: 13443, DNSPort: 13053})
	ironProxyState.put("B", projectInfo{HTTPPort: 14080, HTTPSPort: 14443, DNSPort: 14053})

	peersState.setPeers("A", []string{"B"})
	peersState.setPeers("C", []string{"B"})
	peersState.setPeers("U", []string{}) // declared, no peers

	// t.TempDir() lives under /var/folders/..., whose path alone can
	// exceed the macOS 104-char unix-socket limit; use /tmp instead.
	dir, err := os.MkdirTemp("/tmp", "peers-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	gotA := recordingSoftnet(t, filepath.Join(dir, "a.sock"))
	gotC := recordingSoftnet(t, filepath.Join(dir, "c.sock"))
	gotU := recordingSoftnet(t, filepath.Join(dir, "u.sock"))
	softnetState.put("A", filepath.Join(dir, "a.sock"))
	softnetState.put("C", filepath.Join(dir, "c.sock"))
	softnetState.put("U", filepath.Join(dir, "u.sock"))

	const ntpPort = 51234
	propagatePeerRebind("B", ntpPort)

	lineA := mustRecv(t, gotA, "A")
	assert.Contains(t, lineA, `"op":"setPolicy"`)
	assert.Contains(t, lineA, `"policy":"FORWARDING"`)
	// A's push must carry A's own ports — endpointFrom is called with
	// each dependent's projectInfo, so the dependent sees ITS iron-proxy
	// addresses, not B's.
	assert.Contains(t, lineA, `"http":"127.0.0.1:11080"`)
	assert.Contains(t, lineA, `"ntp":"127.0.0.1:51234"`)

	lineC := mustRecv(t, gotC, "C")
	assert.Contains(t, lineC, `"http":"127.0.0.1:12080"`)

	// U did not list B — it must not receive anything.
	select {
	case line := <-gotU:
		t.Fatalf("unrelated project U received an unexpected push: %s", line)
	default:
	}
}

// TestPropagatePeerRebind_SkipsDependentWithNoSoftnetSock covers a
// dependent that has a projectInfo entry but no softnet control
// socket — e.g. the dependent's VM was torn down between setPeers and
// the rebind that triggers this fanout. The push must silently skip,
// never dial a nonexistent socket.
func TestPropagatePeerRebind_SkipsDependentWithNoSoftnetSock(t *testing.T) {
	origIron := ironProxyState
	origSoftnet := softnetState
	origPeers := peersState
	ironProxyState = newIronProxyStore()
	softnetState = newSoftnetStore()
	peersState = newPeersIndex()
	t.Cleanup(func() {
		ironProxyState = origIron
		softnetState = origSoftnet
		peersState = origPeers
	})

	// A declares B as a peer and has an ironProxyState entry but no
	// softnetState entry — propagate must not panic or hang.
	peersState.setPeers("A", []string{"B"})
	ironProxyState.put("A", projectInfo{HTTPPort: 11080})

	assert.NotPanics(t, func() {
		propagatePeerRebind("B", 51234)
	})
}

// recordingSoftnet stands in for a running softnet child: it accepts
// one connection on the given socket, reads the first line, and
// publishes it to the returned channel. The listener is closed on test
// cleanup so the goroutine exits.
func recordingSoftnet(t *testing.T, sock string) chan string {
	t.Helper()
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })
	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		line, _ := bufio.NewReader(c).ReadString('\n')
		got <- strings.TrimRight(line, "\n")
	}()
	return got
}

func mustRecv(t *testing.T, ch chan string, name string) string {
	t.Helper()
	select {
	case line := <-ch:
		return line
	case <-time.After(2 * time.Second):
		t.Fatalf("%s: no setPolicy received within 2s", name)
	}
	return ""
}
