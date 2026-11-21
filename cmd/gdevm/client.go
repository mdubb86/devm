package main

import (
	"net/http"
	"os"
	"time"
)

// clientURL returns the full URL for a gdevm serve endpoint path.
// Points at 127.0.0.1:8940 by default — the same loopback-reachable
// address gdevm serve binds — and honors DEVM_GDEVM_SERVE_URL so
// tests can point at an httptest.NewServer URL.
func clientURL(path string) string {
	if base := os.Getenv("DEVM_GDEVM_SERVE_URL"); base != "" {
		return base + path
	}
	return "http://127.0.0.1:8940" + path
}

// clientDo is the HTTP client gdevm run/stop/runs/logs use. No
// per-request timeout: /v1/run (attached) and /v1/logs?follow=
// stream indefinitely, and the per-call context already handles
// SIGINT cancellation. Short timeouts belong in the calling site
// when appropriate.
var clientDo = (&http.Client{
	Timeout: 0,
}).Do

// shortClientTimeout is a reasonable deadline for one-shot
// non-streaming endpoints (/v1/stop, /v1/runs). The streaming
// endpoints must not use this.
const shortClientTimeout = 10 * time.Second
