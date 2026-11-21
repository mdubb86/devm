package main

import "sync"

// This file holds serveMain's test-observability seam, kept apart from
// serve.go so the daemon's actual startup/shutdown logic isn't mixed
// with test-only plumbing. Nothing here does anything in production:
// the address is never read outside tests, and the hook is nil unless a
// test sets it.

var (
	servedAddrMu sync.Mutex
	servedAddr   string
)

// setServeAddrForTest records the actual bound address once the
// listener starts. It's harmless in production (any caller could read
// the current bound addr) but only tests care, since
// DEVM_GDEVM_SERVE_ADDR=127.0.0.1:0 picks an ephemeral port whose
// number isn't known until net.Listen returns.
func setServeAddrForTest(addr string) {
	servedAddrMu.Lock()
	defer servedAddrMu.Unlock()
	servedAddr = addr
}

// readServeAddrForTest returns the last address a running serveMain
// bound, or "" if none is currently up.
func readServeAddrForTest() string {
	servedAddrMu.Lock()
	defer servedAddrMu.Unlock()
	return servedAddr
}

// addrPublishedHookForTest, when non-nil, is invoked synchronously by
// serveMain immediately after it publishes the bound address via
// setServeAddrForTest — the same instant a test polling
// readServeAddrForTest would first observe it. A test uses this to send
// itself a signal from exactly that point in serveMain's own goroutine,
// which proves deterministically whether the shutdown signal handler
// was already registered by then: if it wasn't, the signal hits
// OS-default disposition and kills the process outright rather than
// being caught. Always nil outside tests.
var addrPublishedHookForTest func()
