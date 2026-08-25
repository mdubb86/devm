package serviceapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/schema"
)

func TestRoutes_Apply_AddsEntries(t *testing.T) {
	r := NewRoutes()
	require.NoError(t, r.Apply("p1", []Route{
		{Hostname: "app.test", BackendPort: 51001, Mode: ModeVM},
	}))
	got, ok := r.Lookup("app.test", "")
	assert.True(t, ok)
	assert.Equal(t, 51001, got.BackendPort)
	assert.Equal(t, ModeVM, got.Mode)
}

func TestRoutes_Apply_ReplacesProjectEntries(t *testing.T) {
	r := NewRoutes()
	require.NoError(t, r.Apply("p1", []Route{
		{Hostname: "app.test", BackendPort: 51001, Mode: ModeVM, Project: "p1"},
		{Hostname: "api.test", BackendPort: 51002, Mode: ModeVM, Project: "p1"},
	}))
	require.NoError(t, r.Apply("p1", []Route{
		{Hostname: "app.test", BackendPort: 51001, Mode: ModeLocal, Project: "p1"},
	}))
	_, ok := r.Lookup("api.test", "")
	assert.False(t, ok, "api.test should have been removed")
	got, ok := r.Lookup("app.test", "")
	assert.True(t, ok)
	assert.Equal(t, ModeLocal, got.Mode)
}

func TestRoutes_Apply_DoesNotTouchOtherProjects(t *testing.T) {
	r := NewRoutes()
	require.NoError(t, r.Apply("p1", []Route{{Hostname: "p1.test", BackendPort: 51001, Mode: ModeVM}}))
	require.NoError(t, r.Apply("p2", []Route{{Hostname: "p2.test", BackendPort: 51002, Mode: ModeVM}}))
	require.NoError(t, r.Apply("p1", []Route{{Hostname: "p1-new.test", BackendPort: 51003, Mode: ModeVM}}))

	_, ok := r.Lookup("p2.test", "")
	assert.True(t, ok, "p2 routes should be untouched when p1 re-applies")
}

func TestRoutes_Remove_DropsProjectEntries(t *testing.T) {
	r := NewRoutes()
	require.NoError(t, r.Apply("p1", []Route{{Hostname: "app.test", BackendPort: 51001, Mode: ModeVM}}))
	require.NoError(t, r.Apply("p2", []Route{{Hostname: "other.test", BackendPort: 51002, Mode: ModeVM}}))
	r.Remove("p1")
	_, ok := r.Lookup("app.test", "")
	assert.False(t, ok)
	_, ok = r.Lookup("other.test", "")
	assert.True(t, ok, "removing p1 must not touch p2")
}

func TestRoutes_BackendHost_PreservedInLookup(t *testing.T) {
	r := NewRoutes()
	require.NoError(t, r.Apply("p1", []Route{
		{Hostname: "app.test", BackendHost: "192.168.64.5", BackendPort: 3000, Mode: ModeVM},
	}))
	got, ok := r.Lookup("app.test", "")
	assert.True(t, ok)
	assert.Equal(t, "192.168.64.5", got.BackendHost)
	assert.Equal(t, 3000, got.BackendPort)
}

func TestRoutesLookupExcludesDirect(t *testing.T) {
	r := NewRoutes()
	require.NoError(t, r.Apply("proj", []Route{
		{Hostname: "web.test", BackendPort: 8080, Mode: ModeVM, Project: "proj"},
		{Hostname: "db.test", BackendPort: 54322, Direct: true, Project: "proj"},
	}))

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
	require.NoError(t, r.Apply("p1", []Route{
		{Hostname: "app.test", BackendPort: 51001, Mode: ModeVM, Project: "p1"},
	}))

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
	require.NoError(t, r.Apply("p1", []Route{{Hostname: "app.test", BackendPort: 51001, Mode: ModeVM, Project: "p1"}}))

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); r.Lookup("app.test", "") }()
		go func() {
			defer wg.Done()
			_ = r.Apply("p1", []Route{{Hostname: "app.test", BackendPort: 51001, Mode: ModeVM, Project: "p1"}})
		}()
	}
	wg.Wait()
}

func TestApplyRoutes_SubstitutesProjectIP_ForVMNonDirect(t *testing.T) {
	// Set up ironProxyState with a project allocated 127.42.0.7.
	ironProxyState = newIronProxyStore()
	ironProxyState.put("proj-a", projectInfo{ProjectIP: "127.42.0.7"})
	t.Cleanup(func() { ironProxyState = newIronProxyStore() })

	routes := NewRoutes()
	srv := &Server{mux: http.NewServeMux()}
	proxy := NewProxyServer(identity.Prod, routes, nil)
	RegisterRoutesHandlers(srv, identity.Prod, routes, proxy)

	// Client sends a vm-mode non-direct route with NO BackendHost.
	req := ApplyRequest{
		Name: "proj-a",
		Routes: []Route{
			{Hostname: "api.test", BackendPort: 8080, Mode: ModeVM, Project: "proj-a"},
		},
	}
	body, _ := json.Marshal(req)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, httptest.NewRequest("POST", "/routes/apply", bytes.NewReader(body)))

	require.Equal(t, http.StatusOK, rr.Code, "want 200, got %d: %s", rr.Code, rr.Body.String())

	var resp ApplyResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Len(t, resp.Routes, 1)
	assert.Equal(t, "127.42.0.7", resp.Routes[0].BackendHost,
		"BackendHost must be substituted to the project's allocated IP")

	// The stored route (what proxy.go will dial) has the same substituted value.
	stored, ok := routes.Lookup("api.test", "proj-a")
	require.True(t, ok)
	assert.Equal(t, "127.42.0.7", stored.BackendHost)
}

func TestApplyRoutes_ErrorsWhenProjectIPUnallocated(t *testing.T) {
	// ironProxyState has no entry for "proj-b" — VM never started.
	ironProxyState = newIronProxyStore()
	t.Cleanup(func() { ironProxyState = newIronProxyStore() })

	routes := NewRoutes()
	srv := &Server{mux: http.NewServeMux()}
	proxy := NewProxyServer(identity.Prod, routes, nil)
	RegisterRoutesHandlers(srv, identity.Prod, routes, proxy)

	req := ApplyRequest{
		Name: "proj-b",
		Routes: []Route{
			{Hostname: "api.test", BackendPort: 8080, Mode: ModeVM, Project: "proj-b"},
		},
	}
	body, _ := json.Marshal(req)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, httptest.NewRequest("POST", "/routes/apply", bytes.NewReader(body)))

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "no projectIP allocated")
	assert.Contains(t, rr.Body.String(), "proj-b")
	assert.Contains(t, rr.Body.String(), "devm start",
		"error must include the fix hint")

	// Nothing was stored.
	_, ok := routes.Lookup("api.test", "proj-b")
	assert.False(t, ok, "no route should be stored when substitution fails")
}

func TestApplyRoutes_LocalModePassthrough(t *testing.T) {
	// Local-mode routes are untouched — BackendHost stays as CLI set (or unset).
	ironProxyState = newIronProxyStore()
	t.Cleanup(func() { ironProxyState = newIronProxyStore() })

	routes := NewRoutes()
	srv := &Server{mux: http.NewServeMux()}
	proxy := NewProxyServer(identity.Prod, routes, nil)
	RegisterRoutesHandlers(srv, identity.Prod, routes, proxy)

	req := ApplyRequest{
		Name: "proj-c",
		Routes: []Route{
			{Hostname: "api.test", BackendPort: 8080, Mode: ModeLocal, Project: "proj-c"},
		},
	}
	body, _ := json.Marshal(req)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, httptest.NewRequest("POST", "/routes/apply", bytes.NewReader(body)))

	require.Equal(t, http.StatusOK, rr.Code)
	stored, _ := routes.Lookup("api.test", "proj-c")
	assert.Empty(t, stored.BackendHost, "local mode leaves BackendHost as sent")
}

func TestApplyRoutes_DirectVMPassthrough(t *testing.T) {
	// Direct services in vm mode are NOT substituted — DNS handles them.
	ironProxyState = newIronProxyStore()
	ironProxyState.put("proj-d", projectInfo{ProjectIP: "127.42.0.9"})
	t.Cleanup(func() { ironProxyState = newIronProxyStore() })

	routes := NewRoutes()
	srv := &Server{mux: http.NewServeMux()}
	proxy := NewProxyServer(identity.Prod, routes, nil)
	RegisterRoutesHandlers(srv, identity.Prod, routes, proxy)

	req := ApplyRequest{
		Name: "proj-d",
		Routes: []Route{
			{Hostname: "db.test", BackendPort: 5432, Mode: ModeVM, Direct: true, Project: "proj-d"},
		},
	}
	body, _ := json.Marshal(req)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, httptest.NewRequest("POST", "/routes/apply", bytes.NewReader(body)))

	require.Equal(t, http.StatusOK, rr.Code)
	var resp ApplyResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Len(t, resp.Routes, 1)
	assert.Empty(t, resp.Routes[0].BackendHost, "direct routes are not substituted")
}

func TestRoutes_Apply_CollisionRejectsSecondProject(t *testing.T) {
	r := NewRoutes()
	err := r.Apply("alpha", []Route{{Hostname: "api.shared.test", Project: "alpha"}})
	require.NoError(t, err)

	err = r.Apply("beta", []Route{{Hostname: "api.shared.test", Project: "beta"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "api.shared.test")
	assert.Contains(t, err.Error(), "alpha") // names owning project
	assert.Contains(t, err.Error(), "beta")  // names attempting project

	// Alpha's route survives — Apply was atomic.
	got, ok := r.hostnameToRoute["api.shared.test"]
	require.True(t, ok)
	assert.Equal(t, "alpha", got.Project)
}

func TestRoutes_Apply_SameProjectReapplyAllowed(t *testing.T) {
	r := NewRoutes()
	require.NoError(t, r.Apply("alpha", []Route{{Hostname: "api.alpha.test", Project: "alpha"}}))
	// Reapply with a different set — allowed.
	require.NoError(t, r.Apply("alpha", []Route{{Hostname: "web.alpha.test", Project: "alpha"}}))
	_, oldOK := r.hostnameToRoute["api.alpha.test"]
	_, newOK := r.hostnameToRoute["web.alpha.test"]
	assert.False(t, oldOK, "reapply should clear old hostnames")
	assert.True(t, newOK)
}

func TestRoutes_Apply_LANMapPopulatedForExposeHost(t *testing.T) {
	r := NewRoutes()
	require.NoError(t, r.Apply("alpha", []Route{
		{Hostname: "public.alpha.test", Project: "alpha", ExposeHost: true},
		{Hostname: "private.alpha.test", Project: "alpha", ExposeHost: false},
	}))
	_, publicLAN := r.LANLookup("public.alpha.test")
	_, privateLAN := r.LANLookup("private.alpha.test")
	assert.True(t, publicLAN, "ExposeHost=true should register in LAN map")
	assert.False(t, privateLAN, "ExposeHost=false should NOT be in LAN map")
	assert.Equal(t, 1, r.CountLANRoutes())
}

func TestRoutes_Remove_ClearsLANMap(t *testing.T) {
	r := NewRoutes()
	require.NoError(t, r.Apply("alpha", []Route{{Hostname: "x.alpha.test", Project: "alpha", ExposeHost: true}}))
	assert.Equal(t, 1, r.CountLANRoutes())
	r.Remove("alpha")
	assert.Equal(t, 0, r.CountLANRoutes())
	_, ok := r.LANLookup("x.alpha.test")
	assert.False(t, ok)
}

// TestApplyRoutes_MirrorsResolvedToSnapshot pins the write side of the
// daemon-restart recovery contract: /routes/apply must persist the
// resolved route set (with BackendHost substitution applied) into the
// project's state snapshot, so a subsequent daemon restart can
// recoverProjectState → routes.Apply(snap.Routes) verbatim.
func TestApplyRoutes_MirrorsResolvedToSnapshot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ironProxyState = newIronProxyStore()
	ironProxyState.put("mirror-proj", projectInfo{ProjectIP: "127.42.0.7"})
	t.Cleanup(func() { ironProxyState = newIronProxyStore() })

	// A snapshot must exist for the mirror to land — /vm/start writes
	// it in production; the test seeds an empty one to stand in.
	require.NoError(t, WriteStateSnapshot(identity.Prod, "mirror-proj", StateSnapshot{
		Cfg:       schema.Config{Project: schema.Project{Name: "mirror-proj"}},
		ProjectIP: "127.42.0.7",
	}))

	routes := NewRoutes()
	srv := &Server{mux: http.NewServeMux()}
	proxy := NewProxyServer(identity.Prod, routes, nil)
	RegisterRoutesHandlers(srv, identity.Prod, routes, proxy)

	req := ApplyRequest{
		Name: "mirror-proj",
		Routes: []Route{
			{Hostname: "api.mirror-proj.test", BackendPort: 8080, Mode: ModeVM, Project: "mirror-proj"},
			{Hostname: "db.mirror-proj.test", BackendPort: 5432, Mode: ModeVM, Direct: true, Project: "mirror-proj"},
		},
	}
	body, _ := json.Marshal(req)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, httptest.NewRequest("POST", "/routes/apply", bytes.NewReader(body)))
	require.Equal(t, http.StatusOK, rr.Code, "want 200, got %d: %s", rr.Code, rr.Body.String())

	snap, err := ReadStateSnapshot(identity.Prod, "mirror-proj")
	require.NoError(t, err)
	require.NotNil(t, snap)
	require.Len(t, snap.Routes, 2, "both routes must be mirrored into the snapshot")

	byHost := map[string]Route{}
	for _, rt := range snap.Routes {
		byHost[rt.Hostname] = rt
	}
	assert.Equal(t, "127.42.0.7", byHost["api.mirror-proj.test"].BackendHost,
		"snapshot must carry the substituted BackendHost — recovery replays this verbatim")
	assert.Equal(t, ModeVM, byHost["api.mirror-proj.test"].Mode)
	assert.True(t, byHost["db.mirror-proj.test"].Direct, "Direct flag must survive the mirror")
	assert.Empty(t, byHost["db.mirror-proj.test"].BackendHost, "direct routes carry no BackendHost — snapshot must match")
}

// TestApplyRoutes_MirrorSurvivesMissingSnapshot pins the best-effort
// contract: /routes/apply against a project whose /vm/start has not
// written a snapshot yet still succeeds (200) rather than failing the
// whole request. The next /vm/start writes a snapshot, and the following
// /routes/apply back-fills the mirror.
func TestApplyRoutes_MirrorSurvivesMissingSnapshot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ironProxyState = newIronProxyStore()
	ironProxyState.put("no-snap-proj", projectInfo{ProjectIP: "127.42.0.8"})
	t.Cleanup(func() { ironProxyState = newIronProxyStore() })

	routes := NewRoutes()
	srv := &Server{mux: http.NewServeMux()}
	proxy := NewProxyServer(identity.Prod, routes, nil)
	RegisterRoutesHandlers(srv, identity.Prod, routes, proxy)

	req := ApplyRequest{
		Name:   "no-snap-proj",
		Routes: []Route{{Hostname: "api.no-snap-proj.test", BackendPort: 8080, Mode: ModeVM, Project: "no-snap-proj"}},
	}
	body, _ := json.Marshal(req)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, httptest.NewRequest("POST", "/routes/apply", bytes.NewReader(body)))
	assert.Equal(t, http.StatusOK, rr.Code, "a failed snapshot mirror must not fail the apply")

	// The route landed in the live table even though there was
	// nowhere to mirror it.
	_, ok := routes.Lookup("api.no-snap-proj.test", "no-snap-proj")
	assert.True(t, ok)
}

// TestRemoveRoutes_ClearsSnapshotRoutes pins that /routes/remove wipes
// snap.Routes so the next daemon restart replays nothing for the
// project (matching the removed live-table state).
func TestRemoveRoutes_ClearsSnapshotRoutes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ironProxyState = newIronProxyStore()
	ironProxyState.put("clear-proj", projectInfo{ProjectIP: "127.42.0.9"})
	t.Cleanup(func() { ironProxyState = newIronProxyStore() })

	require.NoError(t, WriteStateSnapshot(identity.Prod, "clear-proj", StateSnapshot{
		Cfg:       schema.Config{Project: schema.Project{Name: "clear-proj"}},
		ProjectIP: "127.42.0.9",
		Routes: []Route{
			{Hostname: "api.clear-proj.test", BackendHost: "127.42.0.9", BackendPort: 8080, Mode: ModeVM, Project: "clear-proj"},
		},
	}))

	routes := NewRoutes()
	require.NoError(t, routes.Apply("clear-proj", []Route{
		{Hostname: "api.clear-proj.test", BackendHost: "127.42.0.9", BackendPort: 8080, Mode: ModeVM, Project: "clear-proj"},
	}))
	srv := &Server{mux: http.NewServeMux()}
	proxy := NewProxyServer(identity.Prod, routes, nil)
	RegisterRoutesHandlers(srv, identity.Prod, routes, proxy)

	body, _ := json.Marshal(RemoveRequest{Name: "clear-proj"})
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, httptest.NewRequest("POST", "/routes/remove", bytes.NewReader(body)))
	require.Equal(t, http.StatusNoContent, rr.Code)

	snap, err := ReadStateSnapshot(identity.Prod, "clear-proj")
	require.NoError(t, err)
	require.NotNil(t, snap)
	assert.Empty(t, snap.Routes, "remove must wipe snap.Routes so daemon restart replays nothing")
}
