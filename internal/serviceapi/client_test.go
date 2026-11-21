package serviceapi

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_Health_ReturnsNoErrorWhenServerReachable(t *testing.T) {
	srv, cleanup := newTestServer(t)
	defer cleanup()
	c := NewClientWithSocket(srv.socketPath)
	require.NoError(t, c.Health(context.Background()))
}

func TestClient_Version_ReturnsServerVersion(t *testing.T) {
	srv, cleanup := newTestServer(t)
	defer cleanup()
	c := NewClientWithSocket(srv.socketPath)
	v, err := c.Version(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "test-version", v)
}

func TestClient_Available_TrueWhenServerUp(t *testing.T) {
	srv, cleanup := newTestServer(t)
	defer cleanup()
	c := NewClientWithSocket(srv.socketPath)
	assert.True(t, c.Available(context.Background()))
}

func TestClient_Available_FalseWhenNoServer(t *testing.T) {
	dir := t.TempDir()
	c := NewClientWithSocket(filepath.Join(dir, "nonexistent.sock"))
	assert.False(t, c.Available(context.Background()))
}
