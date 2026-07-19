package portbinder

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shortTempDir returns a fresh temp directory whose path does not embed
// the test name. t.TempDir() nests under a directory named for the
// calling test, and this package's longer test names push UDS socket
// paths past macOS's ~104-byte UNIX_PATH_MAX (sockaddr_un.sun_path),
// causing "bind: invalid argument". os.MkdirTemp keeps the path short.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "pb")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// mockHelper starts a UDS listener that mimics the portbinder helper:
// reads one JSON request, binds a real TCP socket on the requested
// address, sends back the FD via SCM_RIGHTS.
func mockHelper(t *testing.T) string {
	t.Helper()
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "helper.sock")
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		uc := conn.(*net.UnixConn)
		buf := make([]byte, 4096)
		n, _ := uc.Read(buf)
		var req struct {
			Op   string `json:"op"`
			IP   string `json:"ip"`
			Port int    `json:"port"`
		}
		_ = json.Unmarshal(buf[:n], &req)
		// Bind a real TCP socket for the test; use port 0 to get an
		// ephemeral one — the test doesn't verify the exact port, only
		// that we get a working listener back.
		fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
		require.NoError(t, err)
		addr := &syscall.SockaddrInet4{Port: 0}
		copy(addr.Addr[:], []byte{127, 0, 0, 1})
		require.NoError(t, syscall.Bind(fd, addr))
		require.NoError(t, syscall.Listen(fd, 8))
		defer syscall.Close(fd)
		resp := []byte(`{"ok":true}`)
		oob := syscall.UnixRights(fd)
		_, _, _ = uc.WriteMsgUnix(resp, oob, nil)
	}()
	t.Cleanup(func() {
		ln.Close()
		os.Remove(sock)
	})
	return sock
}

// mockHelperNewlineDelimited is a copy of mockHelper whose read side uses
// bufio.NewReader(uc).ReadBytes('\n'), matching the real helper's
// protocol (cmd/devm-portbinder reads with br.ReadBytes('\n')). This
// proves the client terminates its request with a newline as the real
// helper requires.
func mockHelperNewlineDelimited(t *testing.T) string {
	t.Helper()
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "helper.sock")
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		uc := conn.(*net.UnixConn)
		line, err := bufio.NewReader(uc).ReadBytes('\n')
		if err != nil {
			return
		}
		var req struct {
			Op   string `json:"op"`
			IP   string `json:"ip"`
			Port int    `json:"port"`
		}
		_ = json.Unmarshal(line, &req)
		fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
		require.NoError(t, err)
		addr := &syscall.SockaddrInet4{Port: 0}
		copy(addr.Addr[:], []byte{127, 0, 0, 1})
		require.NoError(t, syscall.Bind(fd, addr))
		require.NoError(t, syscall.Listen(fd, 8))
		defer syscall.Close(fd)
		resp := []byte(`{"ok":true}`)
		oob := syscall.UnixRights(fd)
		_, _, _ = uc.WriteMsgUnix(resp, oob, nil)
	}()
	t.Cleanup(func() {
		ln.Close()
		os.Remove(sock)
	})
	return sock
}

func TestBindTCPViaSock_ReturnsWorkingListener(t *testing.T) {
	sock := mockHelper(t)
	ln, err := bindTCPViaSock(sock, "127.0.0.1", 0)
	require.NoError(t, err)
	defer ln.Close()

	addr := ln.Addr().(*net.TCPAddr)
	assert.NotZero(t, addr.Port, "expected ephemeral port assigned")

	// Sanity check: accept a real connection through the FD-passed listener.
	go func() {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			return
		}
		_, _ = c.Write([]byte("ok"))
		c.Close()
	}()
	c, err := ln.Accept()
	require.NoError(t, err)
	defer c.Close()
	buf := make([]byte, 8)
	n, _ := c.Read(buf)
	assert.Equal(t, "ok", string(buf[:n]))
}

// TestBindTCPViaSock_NewlineDelimitedHelper_ReturnsWorkingListener proves
// bindTCPViaSock terminates its request with '\n', matching the real
// helper's bufio.ReadBytes('\n') read loop. Without the trailing
// newline this test hangs until the helper's read times out/blocks.
func TestBindTCPViaSock_NewlineDelimitedHelper_ReturnsWorkingListener(t *testing.T) {
	sock := mockHelperNewlineDelimited(t)
	ln, err := bindTCPViaSock(sock, "127.0.0.1", 0)
	require.NoError(t, err)
	defer ln.Close()

	addr := ln.Addr().(*net.TCPAddr)
	assert.NotZero(t, addr.Port, "expected ephemeral port assigned")

	go func() {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			return
		}
		_, _ = c.Write([]byte("ok"))
		c.Close()
	}()
	c, err := ln.Accept()
	require.NoError(t, err)
	defer c.Close()
	buf := make([]byte, 8)
	n, _ := c.Read(buf)
	assert.Equal(t, "ok", string(buf[:n]))
}

func TestBindTCPViaSock_HelperErrorSurfaces(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "helper.sock")
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = conn.Write([]byte(`{"ok":false,"error":"ip not in devm pool"}`))
	}()
	defer ln.Close()
	defer os.Remove(sock)

	_, err = bindTCPViaSock(sock, "10.0.0.1", 80)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in devm pool")
}
