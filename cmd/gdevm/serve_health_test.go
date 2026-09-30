package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestServeHealth_Returns200WithUptime pins the probe target: /v1/health
// returns 200, JSON body has ok=true, uptime_seconds is a positive
// integer, pid matches os.Getpid().
func TestServeHealth_Returns200WithUptime(t *testing.T) {
	started := time.Now().Add(-1500 * time.Millisecond) // pretend startup 1.5s ago
	h := healthHandler(started)

	req := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var body struct {
		OK            bool `json:"ok"`
		UptimeSeconds int  `json:"uptime_seconds"`
		PID           int  `json:"pid"`
	}
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	assert.True(t, body.OK)
	assert.GreaterOrEqual(t, body.UptimeSeconds, 1)
	assert.Equal(t, os.Getpid(), body.PID)
}

// TestServeMain_RegistersHealthEndpoint pins the wiring: a running
// serveMain actually serves /v1/health, not just healthHandler in
// isolation.
func TestServeMain_RegistersHealthEndpoint(t *testing.T) {
	t.Setenv("DEVM_GDEVM_SERVE_ADDR", "127.0.0.1:0")
	t.Cleanup(func() { signal.Reset(syscall.SIGTERM, syscall.SIGINT) })

	done := make(chan int)
	go func() { done <- serveMain([]string{}) }()

	deadline := time.Now().Add(3 * time.Second)
	var addr string
	for time.Now().Before(deadline) {
		if a := readServeAddrForTest(); a != "" {
			addr = a
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.NotEmpty(t, addr, "server never became reachable")

	resp, err := http.Get("http://" + addr + "/v1/health")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body healthResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.True(t, body.OK)
	assert.Equal(t, os.Getpid(), body.PID)

	p, _ := os.FindProcess(os.Getpid())
	_ = p.Signal(syscall.SIGTERM)
	select {
	case code := <-done:
		assert.Equal(t, 0, code)
	case <-time.After(3 * time.Second):
		t.Fatal("serve did not exit within 3s of SIGTERM")
	}
}
