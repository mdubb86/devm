package serviceapi

import "testing"

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
	// Host header carrying a port still resolves.
	if got, ok := guestOriginBackend(routes, "api.test:443", "proj", "127.42.0.3"); !ok || got != "127.42.0.3:3000" {
		t.Fatalf("host:port = %q,%v want 127.42.0.3:3000,true", got, ok)
	}
}
