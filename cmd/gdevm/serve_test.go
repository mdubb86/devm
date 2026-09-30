package main

import (
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestServeMain_BindsAndAcceptsShutdown pins that gdevm serve binds
// its port, accepts a shutdown signal, and returns exit 0.
func TestServeMain_BindsAndAcceptsShutdown(t *testing.T) {
	// Bind an ephemeral port via a test-only env var override, so the
	// test doesn't collide with a real gdevm serve on 8940.
	t.Setenv("DEVM_GDEVM_SERVE_ADDR", "127.0.0.1:0")
	// serveMain installs its own SIGTERM/SIGINT handler; reset it on
	// cleanup so later tests in this package aren't signal-sensitive.
	t.Cleanup(func() { signal.Reset(syscall.SIGTERM, syscall.SIGINT) })

	done := make(chan int)
	go func() { done <- serveMain([]string{}) }()

	// Wait for the listener to be up (bounded).
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

	// Send SIGTERM; server should shut down cleanly.
	p, _ := os.FindProcess(os.Getpid())
	_ = p.Signal(syscall.SIGTERM)

	select {
	case code := <-done:
		assert.Equal(t, 0, code)
	case <-time.After(3 * time.Second):
		t.Fatal("serve did not exit within 3s of SIGTERM")
	}
}

// TestServeMain_RejectsArgs pins that gdevm serve takes no arguments
// in v1 and exits 2 rather than silently ignoring them.
func TestServeMain_RejectsArgs(t *testing.T) {
	stderr := captureStderr(t, func() {
		code := serveMain([]string{"--bogus"})
		assert.Equal(t, 2, code)
	})
	assert.Contains(t, stderr, "no arguments accepted")
}

// TestServeMain_SignalRegisteredBeforeListenerBinds pins that
// signal.Notify runs before serveMain's bound address becomes
// observable, closing the window where a SIGTERM/SIGINT arriving early
// would hit OS-default disposition and kill the process instead of
// being caught.
//
// It doesn't race an external signal against goroutine scheduling (as
// TestServeMain_BindsAndAcceptsShutdown effectively does) — that would
// only fail intermittently. Instead it uses addrPublishedHookForTest to
// send SIGTERM to the process synchronously, from inside serveMain's
// own goroutine, at the exact instant the address is published. That
// makes the check deterministic: if signal.Notify had not yet run by
// that point (the pre-fix ordering — Listen, publish, spawn Serve,
// *then* Notify), this signal would hit OS-default disposition and
// crash the test binary outright, not just fail an assertion.
func TestServeMain_SignalRegisteredBeforeListenerBinds(t *testing.T) {
	t.Setenv("DEVM_GDEVM_SERVE_ADDR", "127.0.0.1:0")
	t.Cleanup(func() { signal.Reset(syscall.SIGTERM, syscall.SIGINT) })

	hookRan := make(chan struct{})
	addrPublishedHookForTest = func() {
		defer close(hookRan)
		p, err := os.FindProcess(os.Getpid())
		require.NoError(t, err)
		require.NoError(t, p.Signal(syscall.SIGTERM))
	}
	t.Cleanup(func() { addrPublishedHookForTest = nil })

	done := make(chan int, 1)
	go func() { done <- serveMain([]string{}) }()

	select {
	case <-hookRan:
	case <-time.After(3 * time.Second):
		t.Fatal("serveMain never published its bound address")
	}

	select {
	case code := <-done:
		assert.Equal(t, 0, code)
	case <-time.After(3 * time.Second):
		t.Fatal("serve did not exit within 3s of the signal sent at address-publish time")
	}
}
