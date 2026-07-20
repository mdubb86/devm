package softnet

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/mdubb86/devm/internal/identity"
)

// itoa converts an int to its decimal string form.
func itoa(i int) string { return strconv.Itoa(i) }

// freeTCPPort binds 127.0.0.1:0, reads the OS-assigned port, and releases it.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freeTCPPort: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// hostReachable reports whether something is accepting on 127.0.0.1:port.
func hostReachable(port int) bool {
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", itoa(port)), 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func TestIngressApplyReconciles(t *testing.T) {
	ing := newIngress(identity.Prod, nil) // apply's listener lifecycle doesn't need a live stack
	p1, p2 := freeTCPPort(t), freeTCPPort(t)

	ing.apply([]ExposePort{{GuestPort: 5432, BindIP: "127.0.0.1", HostPort: p1}})
	if !hostReachable(p1) {
		t.Fatalf("after apply, host port %d should be listening", p1)
	}

	// Reconcile: replace p1 with p2.
	ing.apply([]ExposePort{{GuestPort: 5432, BindIP: "127.0.0.1", HostPort: p2}})
	if hostReachable(p1) {
		t.Fatalf("p1 %d should be closed after reconcile", p1)
	}
	if !hostReachable(p2) {
		t.Fatalf("p2 %d should be open after reconcile", p2)
	}

	ing.close()
	if hostReachable(p2) {
		t.Fatalf("p2 %d should be closed after close()", p2)
	}
}

// TestIngressReconcileSameHostPortChangedGuestPort covers the close-and-reopen-
// on-the-same-port branch in apply: HostPort unchanged but GuestPort changed.
// This exercises an OS-timing wrinkle — apply immediately re-Listens on the
// port it just closed, relying on Go's default SO_REUSEADDR.
func TestIngressReconcileSameHostPortChangedGuestPort(t *testing.T) {
	ing := newIngress(identity.Prod, nil) // apply's listener lifecycle doesn't need a live stack
	p := freeTCPPort(t)

	ing.apply([]ExposePort{{GuestPort: 5432, BindIP: "127.0.0.1", HostPort: p}})
	if !hostReachable(p) {
		t.Fatalf("after apply, host port %d should be listening", p)
	}

	// Reconcile: same HostPort, different GuestPort.
	ing.apply([]ExposePort{{GuestPort: 5433, BindIP: "127.0.0.1", HostPort: p}})
	if !hostReachable(p) {
		t.Fatalf("host port %d should still be listening after guest-port change", p)
	}
	ing.mu.Lock()
	el, ok := ing.listeners[p]
	ing.mu.Unlock()
	if !ok {
		t.Fatalf("listener for host port %d should still be tracked", p)
	}
	if el.guestPort != 5433 {
		t.Fatalf("listener for host port %d should map to guest port 5433, got %d", p, el.guestPort)
	}

	ing.close()
	if hostReachable(p) {
		t.Fatalf("host port %d should be closed after close()", p)
	}
}

// mockHelper starts a UDS listener that mimics the root
// devm-helper closely enough for apply()'s low-port branch:
// it reads (and discards) one request, binds a real ephemeral TCP
// socket, and hands the FD back via SCM_RIGHTS. Mirrors
// internal/helper/client_test.go's mockHelper.
func mockHelper(t *testing.T) string {
	t.Helper()
	// os.MkdirTemp (not t.TempDir()) keeps the UDS path short enough to
	// stay under macOS's ~104-byte UNIX_PATH_MAX; t.TempDir() embeds the
	// test name and can overflow it.
	dir, err := os.MkdirTemp("", "pb")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "helper.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen unix %s: %v", sock, err)
	}
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		uc := conn.(*net.UnixConn)
		buf := make([]byte, 4096)
		_, _ = uc.Read(buf)
		fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
		if err != nil {
			return
		}
		addr := &syscall.SockaddrInet4{Port: 0}
		copy(addr.Addr[:], []byte{127, 0, 0, 1})
		if err := syscall.Bind(fd, addr); err != nil {
			return
		}
		if err := syscall.Listen(fd, 8); err != nil {
			return
		}
		defer syscall.Close(fd)
		resp, _ := json.Marshal(struct {
			OK bool `json:"ok"`
		}{OK: true})
		oob := syscall.UnixRights(fd)
		_, _, _ = uc.WriteMsgUnix(resp, oob, nil)
	}()
	t.Cleanup(func() {
		ln.Close()
		os.Remove(sock)
	})
	return sock
}

// TestIngressApplyLowPortUsesHelper proves apply()'s branch for
// HostPort<1024 goes through the helper client rather than a direct
// net.Listen: softnet itself is unprivileged and a direct Listen on a
// low port fails with EACCES on macOS (this is the C1 fix). We can't
// actually bind :22 as a non-root test, so we build ingress under a
// test identity.Config whose HelperSocketPath points at a mock
// helper, and confirm apply() produces a working listener whose
// address is the helper's ephemeral bind (not literally :22) — which
// only happens if the low-port branch dialed the helper instead of
// calling net.Listen directly (a direct net.Listen("tcp", ":22") would
// simply fail with EACCES here, not succeed on a different port).
func TestIngressApplyLowPortUsesHelper(t *testing.T) {
	cfg := identity.Config{Name: "test-ingress", HelperSocketPath: mockHelper(t)}

	ing := newIngress(cfg, nil)
	ing.apply([]ExposePort{{GuestPort: 22, BindIP: "127.42.0.5", HostPort: 22}})

	ing.mu.Lock()
	el, ok := ing.listeners[22]
	ing.mu.Unlock()
	if !ok {
		t.Fatalf("expected a listener tracked for host port 22")
	}
	if !hostReachableAddr(el.ln.Addr().String()) {
		t.Fatalf("listener for host port 22 (via mock helper) should be reachable")
	}

	ing.close()
}

// TestIngressApplyHighPortUsesDirectListen proves apply()'s branch for
// HostPort>=1024 still calls net.Listen directly (no helper
// round-trip): the resulting listener binds literally on the
// requested host port, unlike the helper path above which hands
// back a helper-chosen (ephemeral, in the mock) port.
func TestIngressApplyHighPortUsesDirectListen(t *testing.T) {
	ing := newIngress(identity.Prod, nil)
	p := freeTCPPort(t)

	ing.apply([]ExposePort{{GuestPort: 5432, BindIP: "127.0.0.1", HostPort: p}})

	ing.mu.Lock()
	el, ok := ing.listeners[p]
	ing.mu.Unlock()
	if !ok {
		t.Fatalf("expected a listener tracked for host port %d", p)
	}
	if got := el.ln.Addr().(*net.TCPAddr).Port; got != p {
		t.Fatalf("expected direct listen on port %d, got %d", p, got)
	}

	ing.close()
}

// hostReachable dials the given address directly (used for listeners
// whose bound port isn't known ahead of time, e.g. the mock
// helper's ephemeral socket).
func hostReachableAddr(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}
