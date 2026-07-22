package serviceapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/supervisor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandshake_WithProjectID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	build := Build{Version: "dev", Commit: "abc123", Fingerprint: "fp1"}
	srv := NewServer(identity.Prod.SocketPath(), build)
	sup := supervisor.New(t.TempDir())
	RegisterHandshakeHandler(srv, identity.Prod, build, sup)

	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest("GET", "/handshake?name=p", nil))
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	var resp HandshakeResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, build, resp.Build)
	require.NotNil(t, resp.Proxy)
	assert.Equal(t, ProxyMissing, resp.Proxy.Status)
}

func TestHandshake_NoProjectID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	build := Build{Version: "dev", Commit: "abc123", Fingerprint: "fp1"}
	srv := NewServer(identity.Prod.SocketPath(), build)
	sup := supervisor.New(t.TempDir())
	RegisterHandshakeHandler(srv, identity.Prod, build, sup)

	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest("GET", "/handshake", nil))
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	var resp HandshakeResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, build, resp.Build)
	assert.Nil(t, resp.Proxy)
}
