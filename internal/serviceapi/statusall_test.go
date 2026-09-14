package serviceapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeStatusAllTart reports a fixed running-VM set without shelling
// out to `tart`, mirroring fakeTartList in reconcile_test.go. Only
// consulted for orphan detection now — cache rows carry each tracked
// project's own VM-running state.
type fakeStatusAllTart struct {
	running map[string]bool
}

func (f *fakeStatusAllTart) List(ctx context.Context) ([]tart.VM, error) {
	vms := make([]tart.VM, 0, len(f.running))
	for name, running := range f.running {
		vms = append(vms, tart.VM{Name: name, Running: running})
	}
	return vms, nil
}

func TestStatusAll_RunningWithMissingProxyAndStopped(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	cache := NewStateCache()
	cache.SetVMState("running-proj", VMRunning)
	cache.SetIronProxyHealth("running-proj", ProxyHealth{Status: ProxyMissing})
	cache.SetVMState("stopped-proj", VMStopped)

	srv := NewServer(identity.Prod.SocketPath(), Build{Version: "dev"})
	tr := &fakeStatusAllTart{running: map[string]bool{}}
	RegisterStatusAllHandler(srv, identity.Prod, tr, cache)

	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest("GET", "/status/all", nil))
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	var rows []ProjectStatus
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	require.Len(t, rows, 2)

	byID := map[string]ProjectStatus{}
	for _, r := range rows {
		byID[r.Name] = r
	}

	running := byID["running-proj"]
	assert.Equal(t, "running-proj", running.Name)
	assert.True(t, running.VMRunning)
	assert.Equal(t, ProxyMissing, running.Proxy.Status)

	stopped := byID["stopped-proj"]
	assert.Equal(t, "stopped-proj", stopped.Name)
	assert.False(t, stopped.VMRunning)
}

func TestStatusAll_NoCacheRows_EmptyList(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	srv := NewServer(identity.Prod.SocketPath(), Build{Version: "dev"})
	tr := &fakeStatusAllTart{running: map[string]bool{}}
	RegisterStatusAllHandler(srv, identity.Prod, tr, NewStateCache())

	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest("GET", "/status/all", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var rows []ProjectStatus
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	assert.Empty(t, rows)
}

func TestStatusAll_TartListError_Returns500(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	srv := NewServer(identity.Prod.SocketPath(), Build{Version: "dev"})
	RegisterStatusAllHandler(srv, identity.Prod, erroringTartLister{}, NewStateCache())

	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest("GET", "/status/all", nil))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

type erroringTartLister struct{}

func (erroringTartLister) List(ctx context.Context) ([]tart.VM, error) {
	return nil, errors.New("tart list failed")
}

func TestStatusAll_IncludesMacCwd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cache := NewStateCache()
	cache.SetMacCwd("proj", "/Users/foo/bar")
	cache.SetVMState("proj", VMRunning)

	srv := NewServer(identity.Prod.SocketPath(), Build{Version: "dev"})
	RegisterStatusAllHandler(srv, identity.Prod, &fakeStatusAllTart{running: map[string]bool{}}, cache)

	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest("GET", "/status/all", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	assert.Contains(t, rec.Body.String(), `"mac_cwd":"/Users/foo/bar"`)

	var rows []ProjectStatus
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	require.Len(t, rows, 1)
	assert.Equal(t, "/Users/foo/bar", rows[0].MacCwd)
}

func TestStatusAll_IncludesApproveState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cache := NewStateCache()
	cache.SetApproveState("proj", ApproveStateSummary{
		Diverged:        true,
		CurrentDevmSHA:  "abc",
		ApprovedDevmSHA: "def",
	})

	srv := NewServer(identity.Prod.SocketPath(), Build{Version: "dev"})
	RegisterStatusAllHandler(srv, identity.Prod, &fakeStatusAllTart{running: map[string]bool{}}, cache)

	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest("GET", "/status/all", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	assert.Contains(t, rec.Body.String(), `"approve_state":{"diverged":true}`)

	var rows []ProjectStatus
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	require.Len(t, rows, 1)
	require.NotNil(t, rows[0].ApproveState)
	assert.True(t, rows[0].ApproveState.Diverged)
}

func TestStatusAll_MacCwdAndApproveState_OmittedWhenUnset(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cache := NewStateCache()
	cache.SetVMState("proj", VMRunning)

	srv := NewServer(identity.Prod.SocketPath(), Build{Version: "dev"})
	RegisterStatusAllHandler(srv, identity.Prod, &fakeStatusAllTart{running: map[string]bool{}}, cache)

	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest("GET", "/status/all", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	assert.NotContains(t, rec.Body.String(), "mac_cwd")
	assert.NotContains(t, rec.Body.String(), "approve_state")

	var rows []ProjectStatus
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	require.Len(t, rows, 1)
	assert.Empty(t, rows[0].MacCwd)
	assert.Nil(t, rows[0].ApproveState)
}
