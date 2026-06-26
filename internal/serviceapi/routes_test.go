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
	got, ok := r.Lookup("app.test")
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
	_, ok := r.Lookup("api.test")
	assert.False(t, ok, "api.test should have been removed")
	got, ok := r.Lookup("app.test")
	assert.True(t, ok)
	assert.Equal(t, ModeLocal, got.Mode)
}

func TestRoutes_Apply_DoesNotTouchOtherProjects(t *testing.T) {
	r := NewRoutes()
	r.Apply("p1", []Route{{Hostname: "p1.test", BackendPort: 51001, Mode: ModeVM}})
	r.Apply("p2", []Route{{Hostname: "p2.test", BackendPort: 51002, Mode: ModeVM}})
	r.Apply("p1", []Route{{Hostname: "p1-new.test", BackendPort: 51003, Mode: ModeVM}})

	_, ok := r.Lookup("p2.test")
	assert.True(t, ok, "p2 routes should be untouched when p1 re-applies")
}

func TestRoutes_Remove_DropsProjectEntries(t *testing.T) {
	r := NewRoutes()
	r.Apply("p1", []Route{{Hostname: "app.test", BackendPort: 51001, Mode: ModeVM}})
	r.Apply("p2", []Route{{Hostname: "other.test", BackendPort: 51002, Mode: ModeVM}})
	r.Remove("p1")
	_, ok := r.Lookup("app.test")
	assert.False(t, ok)
	_, ok = r.Lookup("other.test")
	assert.True(t, ok, "removing p1 must not touch p2")
}

func TestRoutes_ConcurrentReadWrite_NoRace(t *testing.T) {
	r := NewRoutes()
	r.Apply("p1", []Route{{Hostname: "app.test", BackendPort: 51001, Mode: ModeVM}})

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); r.Lookup("app.test") }()
		go func() { defer wg.Done(); r.Apply("p1", []Route{{Hostname: "app.test", BackendPort: 51001, Mode: ModeVM}}) }()
	}
	wg.Wait()
}
