package main

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubScanner struct{ n int }

func (s stubScanner) Sessions() int { return s.n }

// runAgent spawns the agent's serve loop on a temp socket path and returns
// the path plus a stop func. The serve loop runs in a goroutine; stop() waits
// for it to exit before returning.
func runAgent(t *testing.T) (string, func()) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "devm-agent.sock")
	ag := NewAgent(sock, time.Hour, stubScanner{0})
	done := make(chan struct{})
	go func() {
		_ = ag.Serve()
		close(done)
	}()
	// Wait for socket to be ready.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.Dial("unix", sock); err == nil {
			c.Close()
			return sock, func() {
				ag.Shutdown()
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Fatalf("agent Serve did not return after Shutdown")
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("agent socket %s never came up", sock)
	return "", nil
}

func roundtrip(t *testing.T, sock, req string) string {
	t.Helper()
	conn, err := net.Dial("unix", sock)
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.Write([]byte(req + "\n"))
	require.NoError(t, err)
	resp, err := bufio.NewReader(conn).ReadString('\n')
	require.NoError(t, err)
	return resp[:len(resp)-1] // strip trailing newline
}

func TestAgentPing(t *testing.T) {
	sock, stop := runAgent(t)
	defer stop()
	assert.Equal(t, "PONG", roundtrip(t, sock, "PING"))
}

func TestAgentStatus(t *testing.T) {
	sock, stop := runAgent(t)
	defer stop()
	resp := roundtrip(t, sock, "STATUS")
	assert.Contains(t, resp, "OK uptime=")
	assert.Contains(t, resp, "idle=")
	assert.Contains(t, resp, "sessions=")
}

func TestAgentUnknownCommand(t *testing.T) {
	sock, stop := runAgent(t)
	defer stop()
	assert.Equal(t, "ERR unknown command", roundtrip(t, sock, "BOGUS"))
}

func TestAgentShutdown(t *testing.T) {
	sock, stop := runAgent(t)
	defer stop() // no-op if already shut down
	assert.Equal(t, "BYE", roundtrip(t, sock, "SHUTDOWN"))
	// Wait a moment for shutdown to complete + socket to be removed.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := net.Dial("unix", sock); err != nil {
			return // socket gone — shutdown completed
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("agent did not shut down after SHUTDOWN")
}

func TestAgentStatusReportsScannerSessions(t *testing.T) {
	// Use os.MkdirTemp with a short prefix to stay within macOS's 104-byte
	// sun_path limit for Unix domain sockets.
	dir, err := os.MkdirTemp("", "dag-*")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "devm-agent.sock")
	ag := NewAgent(sock, 200*time.Millisecond, stubScanner{3})
	done := make(chan struct{})
	go func() { _ = ag.Serve(); close(done) }()
	defer func() {
		ag.Shutdown()
		<-done
	}()
	// Wait for socket.
	upDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(upDeadline) {
		if c, err := net.Dial("unix", sock); err == nil {
			c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Wait for at least one scanner tick (idleTimeout/10 = 20ms, floored to 1s).
	time.Sleep(1100 * time.Millisecond)
	resp := roundtrip(t, sock, "STATUS")
	assert.Contains(t, resp, "sessions=3")
}

func TestAgentDoesNotShutDownWhenIdle(t *testing.T) {
	// Use os.MkdirTemp with a short prefix to stay within macOS's 104-byte
	// sun_path limit for Unix domain sockets.
	dir, err := os.MkdirTemp("", "dag-*")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "devm-agent.sock")
	// Scanner returns 0 — agent sees no activity. With the OLD design this
	// would have shut down after 200ms. The new design intentionally does
	// NOT trigger shutdown — the agent must still be alive after the
	// "would-have-fired" window.
	ag := NewAgent(sock, 200*time.Millisecond, stubScanner{0})
	done := make(chan struct{})
	go func() { _ = ag.Serve(); close(done) }()
	defer func() {
		ag.Shutdown()
		<-done
	}()
	// Wait for socket.
	upDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(upDeadline) {
		if c, err := net.Dial("unix", sock); err == nil {
			c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Wait well past the old idle window.
	time.Sleep(500 * time.Millisecond)
	// Agent must still be reachable.
	assert.Equal(t, "PONG", roundtrip(t, sock, "PING"))
}
