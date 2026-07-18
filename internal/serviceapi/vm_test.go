package serviceapi

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSendSoftnetEnforced verifies that sendSoftnetEnforced, given a
// stashed ironProxyInfo and an ntpPort, flips a project's softnet control
// socket to ENFORCED with an iron_proxy endpoint built entirely from
// loopback addresses — softnet dials iron-proxy host-side, not through a
// vmnet bridge.
//
// Uses os.MkdirTemp (short prefix) rather than t.TempDir: a unix socket
// path is capped at ~104 bytes on macOS, and t.TempDir embeds the full
// test name in the path — long enough here to overflow that limit.
func TestSendSoftnetEnforced(t *testing.T) {
	dir, err := os.MkdirTemp("", "softnet")
	require.NoError(t, err)
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "c.sock")
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

	info := ironProxyInfo{
		HTTPPort:  8080,
		HTTPSPort: 8443,
		DNSPort:   8053,
	}
	err = sendSoftnetEnforced(sock, info, 51234)
	require.NoError(t, err)

	line := <-got
	assert.Contains(t, line, `"op":"setPolicy"`)
	assert.Contains(t, line, `"policy":"ENFORCED"`)
	assert.Contains(t, line, `"http":"127.0.0.1:8080"`)
	assert.Contains(t, line, `"https":"127.0.0.1:8443"`)
	assert.Contains(t, line, `"dns":"127.0.0.1:8053"`)
	assert.Contains(t, line, `"ntp":"127.0.0.1:51234"`)
}
