package main

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestSSHAgentBridgeMain_SplicesBytes(t *testing.T) {
	// Fake Mac bridge: echoes everything.
	macLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer macLn.Close()
	go func() {
		c, _ := macLn.Accept()
		if c == nil {
			return
		}
		defer c.Close()
		_, _ = io.Copy(c, c)
	}()
	macPort := macLn.Addr().(*net.TCPAddr).Port

	// os.MkdirTemp (not t.TempDir()) keeps the UDS path short enough to
	// stay under macOS's ~104-byte sun_path limit; t.TempDir() embeds the
	// test name and overruns it.
	sockDir, err := os.MkdirTemp("", "gdevm-agent-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "ssh-agent.sock")

	// Start the subcommand in-process in a goroutine.
	done := make(chan int, 1)
	go func() {
		done <- sshAgentBridgeMain([]string{
			"--sock", sock,
			"--mac-host", "127.0.0.1",
			"--mac-port", intToStr(macPort),
		})
	}()
	defer func() {
		_ = os.Remove(sock)
	}()

	// Wait for socket to appear.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	msg := []byte("roundtrip bytes")
	if _, err := c.Write(msg); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(msg) {
		t.Fatalf("got %q", got)
	}
}

func intToStr(n int) string {
	return strconv.Itoa(n)
}
