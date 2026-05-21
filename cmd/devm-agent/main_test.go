package main

import (
	"bufio"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runAgent spawns the agent's serve loop on a temp socket path and returns
// the path plus a stop func. The serve loop runs in a goroutine.
func runAgent(t *testing.T) (string, func()) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "devm-agent.sock")
	ag := NewAgent(sock, time.Hour) // long idle window for these tests
	go func() { _ = ag.Serve() }()
	// Wait for socket to be ready.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := net.Dial("unix", sock); err == nil {
			return sock, func() { ag.Shutdown() }
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
}

func TestAgentHeartbeat(t *testing.T) {
	sock, stop := runAgent(t)
	defer stop()
	assert.Equal(t, "OK", roundtrip(t, sock, "HEARTBEAT"))
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

func TestAgentAutoIdle(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "devm-agent.sock")
	// idleTimeout much shorter than the test's deadline.
	ag := NewAgent(sock, 200*time.Millisecond)
	go func() { _ = ag.Serve() }()
	// Wait for it to come up.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := net.Dial("unix", sock); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Send nothing; wait > idleTimeout.
	time.Sleep(500 * time.Millisecond)
	// Now the socket should be gone.
	_, err := net.Dial("unix", sock)
	assert.Error(t, err, "agent should have self-exited after idle timeout")
}
