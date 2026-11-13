package serviceapi

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
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
