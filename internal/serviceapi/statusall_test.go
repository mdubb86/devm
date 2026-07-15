package serviceapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/supervisor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeStatusAllTart reports a fixed running-VM set without shelling
// out to `tart`, mirroring fakeTartList in reconcile_test.go.
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

func writeStatusAllSnapshot(t *testing.T, projectID string, cfg schema.Config) {
	t.Helper()
	require.NoError(t, WriteStateSnapshot(projectID, StateSnapshot{Cfg: cfg}))
}

func TestStatusAll_RunningWithMissingProxyAndStopped(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", t.TempDir())

	writeStatusAllSnapshot(t, "running-proj", schema.Config{
		Project: schema.Project{ID: "running-proj", VMName: "running-proj-vm"},
	})
	writeStatusAllSnapshot(t, "stopped-proj", schema.Config{
		Project: schema.Project{ID: "stopped-proj", VMName: "stopped-proj-vm"},
	})

	srv := NewServer(SocketPath(), Build{Version: "dev"})
	sup := supervisor.New("")
	tr := &fakeStatusAllTart{running: map[string]bool{"running-proj-vm": true}}
	RegisterStatusAllHandler(srv, sup, tr)

	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest("GET", "/status/all", nil))
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	var rows []ProjectStatus
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	require.Len(t, rows, 2)

	byID := map[string]ProjectStatus{}
	for _, r := range rows {
		byID[r.ProjectID] = r
	}

	running := byID["running-proj"]
	assert.Equal(t, "running-proj-vm", running.VMName)
	assert.True(t, running.VMRunning)
	// No live iron-proxy process and no config file on disk for this
	// project — computeProxyHealth reports MISSING.
	assert.Equal(t, ProxyMissing, running.Proxy.Status)

	stopped := byID["stopped-proj"]
	assert.Equal(t, "stopped-proj-vm", stopped.VMName)
	assert.False(t, stopped.VMRunning)
}

func TestStatusAll_NoSnapshots_EmptyList(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", t.TempDir())

	srv := NewServer(SocketPath(), Build{Version: "dev"})
	sup := supervisor.New("")
	tr := &fakeStatusAllTart{running: map[string]bool{}}
	RegisterStatusAllHandler(srv, sup, tr)

	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest("GET", "/status/all", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var rows []ProjectStatus
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	assert.Empty(t, rows)
}

func TestStatusAll_SkipsNonJSONAndMalformedFiles(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", t.TempDir())
	require.NoError(t, os.MkdirAll(StateDir(), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(StateDir(), "notes.txt"), []byte("hi"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(StateDir(), "broken.json"), []byte("{not json"), 0o600))
	writeStatusAllSnapshot(t, "good", schema.Config{Project: schema.Project{ID: "good", VMName: "good-vm"}})

	srv := NewServer(SocketPath(), Build{Version: "dev"})
	sup := supervisor.New("")
	tr := &fakeStatusAllTart{running: map[string]bool{}}
	RegisterStatusAllHandler(srv, sup, tr)

	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest("GET", "/status/all", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var rows []ProjectStatus
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	require.Len(t, rows, 1)
	assert.Equal(t, "good", rows[0].ProjectID)
}

func TestStatusAll_TartListError_Returns500(t *testing.T) {
	t.Setenv("DEVM_RUNTIME_DIR", t.TempDir())

	srv := NewServer(SocketPath(), Build{Version: "dev"})
	sup := supervisor.New("")
	RegisterStatusAllHandler(srv, sup, erroringTartLister{})

	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest("GET", "/status/all", nil))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

type erroringTartLister struct{}

func (erroringTartLister) List(ctx context.Context) ([]tart.VM, error) {
	return nil, errors.New("tart list failed")
}
