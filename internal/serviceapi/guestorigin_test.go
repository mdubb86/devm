package serviceapi

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestGuestOriginBackendPinsToGuest(t *testing.T) {
	routes := NewRoutes()
	if err := routes.Apply("proj", []Route{
		{Hostname: "api.test", BackendPort: 3000, Mode: ModeVM, Project: "proj"},
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	got, ok := guestOriginBackend(routes, "api.test", "proj", "127.42.0.3")
	if !ok || got != "127.42.0.3:3000" {
		t.Fatalf("= %q,%v want 127.42.0.3:3000,true", got, ok)
	}
}

// TestGuestOriginBackendIgnoresLocalMode is the security lock: a project in
// `devm route local` mode must NOT expose the Mac's localhost services to the
// guest. The backend is pinned to the guest regardless of route mode.
func TestGuestOriginBackendIgnoresLocalMode(t *testing.T) {
	routes := NewRoutes()
	if err := routes.Apply("proj", []Route{
		{Hostname: "api.test", BackendHost: "localhost", BackendPort: 3000,
			Mode: ModeLocal, Project: "proj"},
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	got, ok := guestOriginBackend(routes, "api.test", "proj", "127.42.0.3")
	if !ok {
		t.Fatal("local-mode route must still resolve for the guest")
	}
	if got != "127.42.0.3:3000" {
		t.Fatalf("= %q want 127.42.0.3:3000 — local-mode backend leaked to the guest", got)
	}
}

func TestGuestOriginBackendRejects(t *testing.T) {
	routes := NewRoutes()
	if err := routes.Apply("proj", []Route{
		{Hostname: "api.test", BackendPort: 3000, Mode: ModeVM, Project: "proj"},
		{Hostname: "db.test", BackendPort: 5432, Mode: ModeVM, Project: "proj", Direct: true},
		{Hostname: "other.test", BackendPort: 4000, Mode: ModeVM, Project: "elsewhere"},
		{Hostname: "http-pin.test", BackendPort: 80, Mode: ModeVM, Project: "proj"},
		{Hostname: "https-pin.test", BackendPort: 443, Mode: ModeVM, Project: "proj"},
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// Unknown hostname.
	if _, ok := guestOriginBackend(routes, "nope.test", "proj", "127.42.0.3"); ok {
		t.Fatal("unknown hostname must not resolve")
	}
	// Direct services are raw TCP end-to-end; never proxy-dialed.
	if _, ok := guestOriginBackend(routes, "db.test", "proj", "127.42.0.3"); ok {
		t.Fatal("direct service must not resolve through the guest-origin listener")
	}
	// Cross-project isolation.
	if _, ok := guestOriginBackend(routes, "other.test", "proj", "127.42.0.3"); ok {
		t.Fatal("cross-project hostname must not resolve")
	}
	// No project IP allocated.
	if _, ok := guestOriginBackend(routes, "api.test", "proj", ""); ok {
		t.Fatal("missing project IP must not resolve")
	}
	// No project ID — Routes.Lookup with an empty project would skip
	// isolation; the security function must defend itself too (F12).
	if _, ok := guestOriginBackend(routes, "api.test", "", "127.42.0.3"); ok {
		t.Fatal("missing project ID must not resolve")
	}
	// Port 80/443 on projectIP belongs to the browser-facing
	// ProxyServer's own listeners, never a guest service — reflecting
	// guest traffic into that listener would let route-local mode leak
	// a Mac localhost backend to the guest (F5).
	if _, ok := guestOriginBackend(routes, "http-pin.test", "proj", "127.42.0.3"); ok {
		t.Fatal("port 80 must not resolve — reserved for the browser-facing listener")
	}
	if _, ok := guestOriginBackend(routes, "https-pin.test", "proj", "127.42.0.3"); ok {
		t.Fatal("port 443 must not resolve — reserved for the browser-facing listener")
	}
	// Host header carrying a port still resolves.
	if got, ok := guestOriginBackend(routes, "api.test:443", "proj", "127.42.0.3"); !ok || got != "127.42.0.3:3000" {
		t.Fatalf("host:port = %q,%v want 127.42.0.3:3000,true", got, ok)
	}
}

// TestGuestOriginHandlerDispatch drives the guest-origin HTTP handler directly
// against a stub backend, confirming it reaches the guest and reports a clean
// 502 for an unknown host.
func TestGuestOriginHandlerDispatch(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello from the guest")
	}))
	defer backend.Close()

	_, portStr, err := net.SplitHostPort(strings.TrimPrefix(backend.URL, "http://"))
	if err != nil {
		t.Fatalf("split backend addr: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse backend port: %v", err)
	}

	routes := NewRoutes()
	if err := routes.Apply("proj", []Route{
		{Hostname: "api.test", BackendPort: port, Mode: ModeVM, Project: "proj"},
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	h := &guestOriginHandler{routes: routes, projectID: "proj", projectIP: "127.0.0.1"}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "http://api.test/", nil)
	req.Host = "api.test"
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d want 200", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "hello from the guest") {
		t.Fatalf("body = %q", body)
	}

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "http://nope.test/", nil)
	req2.Host = "nope.test"
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusBadGateway {
		t.Fatalf("unknown host status = %d want 502", rec2.Code)
	}
}
