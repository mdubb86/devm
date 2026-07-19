package serviceapi

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRoutes_Apply_AddsEntries(t *testing.T) {
	r := NewRoutes()
	r.Apply("p1", []Route{
		{Hostname: "app.test", BackendPort: 51001, Mode: ModeVM},
	})
	got, ok := r.Lookup("app.test", "")
	assert.True(t, ok)
	assert.Equal(t, 51001, got.BackendPort)
	assert.Equal(t, ModeVM, got.Mode)
}

func TestRoutes_Apply_ReplacesProjectEntries(t *testing.T) {
	r := NewRoutes()
	r.Apply("p1", []Route{
		{Hostname: "app.test", BackendPort: 51001, Mode: ModeVM},
		{Hostname: "api.test", BackendPort: 51002, Mode: ModeVM},
	})
	r.Apply("p1", []Route{
		{Hostname: "app.test", BackendPort: 51001, Mode: ModeLocal},
	})
	_, ok := r.Lookup("api.test", "")
	assert.False(t, ok, "api.test should have been removed")
	got, ok := r.Lookup("app.test", "")
	assert.True(t, ok)
	assert.Equal(t, ModeLocal, got.Mode)
}

func TestRoutes_Apply_DoesNotTouchOtherProjects(t *testing.T) {
	r := NewRoutes()
	r.Apply("p1", []Route{{Hostname: "p1.test", BackendPort: 51001, Mode: ModeVM}})
	r.Apply("p2", []Route{{Hostname: "p2.test", BackendPort: 51002, Mode: ModeVM}})
	r.Apply("p1", []Route{{Hostname: "p1-new.test", BackendPort: 51003, Mode: ModeVM}})

	_, ok := r.Lookup("p2.test", "")
	assert.True(t, ok, "p2 routes should be untouched when p1 re-applies")
}

func TestRoutes_Remove_DropsProjectEntries(t *testing.T) {
	r := NewRoutes()
	r.Apply("p1", []Route{{Hostname: "app.test", BackendPort: 51001, Mode: ModeVM}})
	r.Apply("p2", []Route{{Hostname: "other.test", BackendPort: 51002, Mode: ModeVM}})
	r.Remove("p1")
	_, ok := r.Lookup("app.test", "")
	assert.False(t, ok)
	_, ok = r.Lookup("other.test", "")
	assert.True(t, ok, "removing p1 must not touch p2")
}

func TestRoutes_BackendHost_PreservedInLookup(t *testing.T) {
	r := NewRoutes()
	r.Apply("p1", []Route{
		{Hostname: "app.test", BackendHost: "192.168.64.5", BackendPort: 3000, Mode: ModeVM},
	})
	got, ok := r.Lookup("app.test", "")
	assert.True(t, ok)
	assert.Equal(t, "192.168.64.5", got.BackendHost)
	assert.Equal(t, 3000, got.BackendPort)
}

func TestRoutesLookupExcludesDirect(t *testing.T) {
	r := NewRoutes()
	r.Apply("proj", []Route{
		{Hostname: "web.test", BackendPort: 8080, Mode: ModeVM, Project: "proj"},
		{Hostname: "db.test", BackendPort: 54322, Direct: true, Project: "proj"},
	})

	// Proxy dial path: proxied host resolves, direct host does NOT.
	_, ok := r.Lookup("web.test", "")
	assert.True(t, ok, "proxied route must be dialable")
	_, ok = r.Lookup("db.test", "")
	assert.False(t, ok, "direct route must be excluded from the proxy dial path")

	// DNS path: direct host resolves with its project.
	dr, ok := r.DirectRoute("db.test")
	assert.True(t, ok)
	assert.Equal(t, "proj", dr.Project)
	// A proxied host is not a direct route.
	_, ok = r.DirectRoute("web.test")
	assert.False(t, ok)

	// AllByProject still lists both (for the admin/status view).
	all := r.AllByProject()
	assert.Len(t, all["proj"], 2)
}

func TestRoutes_Lookup_ScopedByProject(t *testing.T) {
	r := NewRoutes()
	r.Apply("p1", []Route{
		{Hostname: "app.test", BackendPort: 51001, Mode: ModeVM, Project: "p1"},
	})

	// Correct project — resolves.
	got, ok := r.Lookup("app.test", "p1")
	assert.True(t, ok)
	assert.Equal(t, 51001, got.BackendPort)

	// Wrong project — isolation guarantee: refused even though the
	// hostname exists in the table.
	_, ok = r.Lookup("app.test", "p2")
	assert.False(t, ok, "a route owned by p1 must not resolve for p2's dest-IP scope")

	// Empty project — skips the scope check (back-compat / DNS-style
	// callers that establish project scope some other way).
	_, ok = r.Lookup("app.test", "")
	assert.True(t, ok)
}

func TestRoutes_ConcurrentReadWrite_NoRace(t *testing.T) {
	r := NewRoutes()
	r.Apply("p1", []Route{{Hostname: "app.test", BackendPort: 51001, Mode: ModeVM}})

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); r.Lookup("app.test", "") }()
		go func() {
			defer wg.Done()
			r.Apply("p1", []Route{{Hostname: "app.test", BackendPort: 51001, Mode: ModeVM}})
		}()
	}
	wg.Wait()
}
