package router

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_GET_ReturnsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/foo", r.URL.Path)
		w.WriteHeader(200)
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()

	c := NewWithURL(srv.URL)
	status, body, err := c.do("GET", "/foo", nil)
	require.NoError(t, err)
	assert.Equal(t, 200, status)
	assert.Equal(t, "hello", body)
}

func TestClient_POST_SendsJSONBody(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		buf, _ := io.ReadAll(r.Body)
		got = string(buf)
		w.WriteHeader(201)
	}))
	defer srv.Close()

	c := NewWithURL(srv.URL)
	status, _, err := c.do("POST", "/bar", map[string]any{"x": 1})
	require.NoError(t, err)
	assert.Equal(t, 201, status)
	assert.True(t, strings.Contains(got, `"x":1`))
}

func TestClient_Unreachable_ErrorMessageGuides(t *testing.T) {
	c := NewWithURL("http://127.0.0.1:1") // unreachable
	_, _, err := c.do("GET", "/", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "caddy admin API not reachable")
	assert.Contains(t, err.Error(), "brew install caddy")
}
