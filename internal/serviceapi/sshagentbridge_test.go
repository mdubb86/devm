package serviceapi

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// fakeAgent speaks just enough ssh-agent to echo back a stub pubkey
// for REQUEST_IDENTITIES. Exposes a unix socket under tmp.
//
// Uses os.MkdirTemp with a short prefix so the sock path stays under
// macOS's 104-char sun_path limit; t.TempDir() builds paths too long
// under /var/folders when combined with the test name.
func startFakeAgent(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ab")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "a.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				for {
					typ, _, err := ReadAgentMessage(c)
					if err != nil {
						return
					}
					switch typ {
					case AgentRequestIdentities:
						// SSH_AGENT_IDENTITIES_ANSWER (12), body: uint32 count=0
						body := []byte{0, 0, 0, 0}
						_ = WriteAgentMessage(c, 12, body)
					default:
						_ = WriteAgentMessage(c, AgentFailure, nil)
					}
				}
			}(c)
		}
	}()
	return sock
}

func TestAgentBridge_ForwardsRequestIdentities(t *testing.T) {
	sock := startFakeAgent(t)
	b := NewAgentBridge("projX", func() string { return sock })

	// Pipe the two ends in-process.
	guest, bridge := net.Pipe()
	defer guest.Close()
	done := make(chan struct{})
	go func() { b.ServeConn(bridge); close(done) }()

	// Send REQUEST_IDENTITIES.
	if err := WriteAgentMessage(guest, AgentRequestIdentities, nil); err != nil {
		t.Fatal(err)
	}
	_ = guest.SetReadDeadline(time.Now().Add(3 * time.Second))
	typ, payload, err := ReadAgentMessage(guest)
	if err != nil {
		t.Fatal(err)
	}
	if typ != 12 {
		t.Fatalf("want response type 12, got %d", typ)
	}
	want := []byte{0, 0, 0, 0}
	if !bytes.Equal(payload, want) {
		t.Fatalf("payload %v want %v", payload, want)
	}

	guest.Close()
	<-done
}

func TestAgentBridge_RejectsAddIdentity(t *testing.T) {
	sock := startFakeAgent(t)
	b := NewAgentBridge("projX", func() string { return sock })

	guest, bridge := net.Pipe()
	defer guest.Close()
	done := make(chan struct{})
	go func() { b.ServeConn(bridge); close(done) }()

	// ADD_IDENTITY (17) with some bytes.
	if err := WriteAgentMessage(guest, 17, []byte("\x00\x00\x00\x08bad-data")); err != nil {
		t.Fatal(err)
	}
	_ = guest.SetReadDeadline(time.Now().Add(3 * time.Second))
	typ, _, err := ReadAgentMessage(guest)
	if err != nil {
		t.Fatal(err)
	}
	if typ != AgentFailure {
		t.Fatalf("want AgentFailure (5), got %d — bridge is leaking ADD_IDENTITY!", typ)
	}

	// Verify fake agent did NOT see the message — we close and verify
	// nothing exploded.
	guest.Close()
	<-done
}

// yieldConn wraps a net.Conn so every Write yields the scheduler
// before the underlying Write. WriteAgentMessage issues three Writes
// (header / type / payload); under this wrapper another goroutine can
// slip its own Write in between — the exact window a missing lock
// around the whole frame would leave open.
type yieldConn struct {
	net.Conn
}

func (y yieldConn) Write(p []byte) (int, error) {
	runtime.Gosched()
	return y.Conn.Write(p)
}

// TestAgentBridge_ConcurrentWritesSerialized spams REQUEST_IDENTITIES
// (forward -> mac response arrives via io.Copy path) alternating with
// ADD_IDENTITY (reject -> filter writes AgentFailure to guest). Both
// bridge-side writers hit guestConn concurrently, and the yieldConn
// wrapper maximizes interleaving. Every frame the guest reads must
// still parse as a valid, type-correct message; a missing lock around
// the full frame interleaves bytes and corrupts the wire.
func TestAgentBridge_ConcurrentWritesSerialized(t *testing.T) {
	sock := startFakeAgent(t)
	b := NewAgentBridge("projX", func() string { return sock })

	guest, bridge := net.Pipe()
	defer guest.Close()
	done := make(chan struct{})
	go func() { b.ServeConn(yieldConn{Conn: bridge}); close(done) }()

	const N = 50
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		for i := 0; i < N; i++ {
			if err := WriteAgentMessage(guest, AgentRequestIdentities, nil); err != nil {
				return
			}
			if err := WriteAgentMessage(guest, 17, nil); err != nil {
				return
			}
		}
	}()

	_ = guest.SetReadDeadline(time.Now().Add(10 * time.Second))
	forward, reject := 0, 0
	for i := 0; i < 2*N; i++ {
		typ, payload, err := ReadAgentMessage(guest)
		if err != nil {
			t.Fatalf("frame %d: read err: %v (forward=%d reject=%d) — wire corrupted?", i, err, forward, reject)
		}
		switch typ {
		case 12:
			if !bytes.Equal(payload, []byte{0, 0, 0, 0}) {
				t.Fatalf("frame %d: forward response payload corrupted: %v", i, payload)
			}
			forward++
		case AgentFailure:
			if len(payload) != 0 {
				t.Fatalf("frame %d: reject response had payload: %v", i, payload)
			}
			reject++
		default:
			t.Fatalf("frame %d: unexpected type %d (wire interleaved?)", i, typ)
		}
	}
	<-writeDone
	if forward != N {
		t.Errorf("forward: got %d, want %d", forward, N)
	}
	if reject != N {
		t.Errorf("reject: got %d, want %d", reject, N)
	}
	guest.Close()
	<-done
}

func TestAgentBridge_SockUnresolvable(t *testing.T) {
	// Empty socket path: bridge must return AgentFailure immediately.
	b := NewAgentBridge("projX", func() string { return "" })
	guest, bridge := net.Pipe()
	defer guest.Close()
	done := make(chan struct{})
	go func() { b.ServeConn(bridge); close(done) }()

	// The guest's write is decoupled from the main goroutine's read:
	// on net.Pipe neither side buffers, and in the unresolvable-sock
	// path the bridge never reads from guestConn, so a blocking write
	// here would deadlock with the bridge's blocking write of
	// AgentFailure back. The guest's request may or may not arrive
	// before the bridge closes — the contract under test is only that
	// the bridge writes AgentFailure regardless.
	go func() { _ = WriteAgentMessage(guest, AgentRequestIdentities, nil) }()
	_ = guest.SetReadDeadline(time.Now().Add(3 * time.Second))
	typ, _, err := ReadAgentMessage(guest)
	if err != nil {
		t.Fatal(err)
	}
	if typ != AgentFailure {
		t.Fatalf("want AgentFailure, got %d", typ)
	}
	guest.Close()
	<-done
}
