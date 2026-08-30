package serviceapi

import (
	"bytes"
	"encoding/json"
	"fmt"
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

// TestVMStop_CallsMutagenStopPhaseBeforeGracefulStop verifies Task 18:
// the /vm/stop handler flushes+pauses the project's mutagen sessions
// (via mutagenStopPhaseFn) BEFORE gracefulStopVM powers the guest off —
// mutagen's transport is tart exec (cmd/tart-mutagen-ssh), which needs the
// guest running, and the poweroff ends that.
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

// TestEndpointFrom_MapsAllFieldsToLoopback verifies endpointFrom — the
// single builder behind every setPolicy push — wires each projectInfo port
// to its own 127.0.0.1:<port> field on the returned Endpoint, with no
// cross-field swaps (e.g. HTTPS getting the DNS port).
func TestEndpointFrom_MapsAllFieldsToLoopback(t *testing.T) {
	info := projectInfo{
		HTTPPort:       5001,
		HTTPSPort:      5002,
		DNSPort:        5003,
		GuestHTTPPort:  5005,
		GuestHTTPSPort: 5006,
		PopPort:        5007,
	}
	const ntpPort = 5004

	ep := endpointFrom(info, ntpPort)

	assert.Equal(t, "127.0.0.1:5001", ep.HTTP)
	assert.Equal(t, "127.0.0.1:5002", ep.HTTPS)
	assert.Equal(t, "127.0.0.1:5003", ep.DNS)
	assert.Equal(t, "127.0.0.1:5004", ep.NTP)
	assert.Equal(t, "127.0.0.1:5005", ep.GuestHTTP)
	assert.Equal(t, "127.0.0.1:5006", ep.GuestHTTPS)
	assert.Equal(t, "127.0.0.1:5007", ep.Pop)
}
