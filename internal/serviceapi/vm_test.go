package serviceapi

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/supervisor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSendSoftnetEnforced verifies that sendSoftnetEnforced, given a
// stashed projectInfo and an ntpPort, flips a project's softnet control
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

	info := projectInfo{
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

// TestVMStop_CallsMutagenStopPhaseBeforeGracefulStop verifies Task 18:
// the /vm/stop handler flushes+pauses the project's mutagen sessions
// (via mutagenStopPhaseFn) BEFORE gracefulStopVM powers the guest off —
// mutagen's SSH transport needs sshd up, which the poweroff ends.
//
// mutagenStopPhaseFn is faked to append a marker into the same log file
// the fake tart binary writes every invocation into, so both events
// land on one ordered timeline — this pins sequencing, not StopPhase's
// own behavior (covered by mutagen_sessions_test.go).
//
// The fake tart binary reports the VM absent on `list` so
// gracefulStopVM's poll returns on its very first check instead of
// waiting out the real 45s grace timeout.
func TestVMStop_CallsMutagenStopPhaseBeforeGracefulStop(t *testing.T) {
	orig := mutagenStopPhaseFn
	defer func() { mutagenStopPhaseFn = orig }()

	t.Setenv("HOME", t.TempDir())
	repoRoot := t.TempDir()
	logPath := filepath.Join(repoRoot, "order.log")

	binPath := filepath.Join(repoRoot, "tart-fake")
	script := fmt.Sprintf(`#!/bin/sh
echo "$*" >> %q
case "$1" in
  list) echo '[]' ;;
esac
exit 0
`, logPath)
	require.NoError(t, os.WriteFile(binPath, []byte(script), 0o755))
	tr := tart.New()
	tr.Path = binPath

	var stopArgs []string
	mutagenStopPhaseFn = func(cfg identity.Config, projectID string) error {
		fh, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		require.NoError(t, err)
		defer fh.Close()
		_, err = fmt.Fprintln(fh, "MUTAGEN-STOP "+projectID)
		require.NoError(t, err)
		stopArgs = append(stopArgs, projectID)
		return nil
	}

	ironProxyState.put("proj-stop", projectInfo{})
	t.Cleanup(func() { ironProxyState.del("proj-stop") })

	server := NewServer(identity.Prod.SocketPath(), Build{})
	locks := NewProjectLocks()
	sup := supervisor.New(t.TempDir())
	RegisterVMHandlers(server, identity.Prod, sup, tr, nil, 0, locks, nil)

	body, err := json.Marshal(VMStopRequest{Name: "proj-stop"})
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	server.mux.ServeHTTP(rec, httptest.NewRequest("POST", "/vm/stop", bytes.NewReader(body)))
	require.Equal(t, http.StatusNoContent, rec.Code, "body=%s", rec.Body.String())

	assert.Equal(t, []string{"proj-stop"}, stopArgs, "StopPhase must be called for the right projectID")

	logBytes, err := os.ReadFile(logPath)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(logBytes)), "\n")
	stopIdx, listIdx := -1, -1
	for i, line := range lines {
		switch {
		case strings.Contains(line, "MUTAGEN-STOP"):
			stopIdx = i
		case listIdx == -1 && strings.HasPrefix(line, "list"):
			listIdx = i
		}
	}
	require.GreaterOrEqual(t, stopIdx, 0, "mutagen stop marker must be present")
	require.GreaterOrEqual(t, listIdx, 0, "gracefulStopVM's tart list call must be present")
	assert.Less(t, stopIdx, listIdx,
		"mutagen sessions must be flushed+paused BEFORE the VM's guest is powered off")
}
