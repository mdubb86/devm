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

func TestEnsureServer_UsesExistingServerOn80(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/config/apps/http/servers" {
			w.Write([]byte(`{"someoneelse":{"listen":[":80"]}}`))
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	c := NewWithURL(srv.URL)
	name, err := c.EnsureServer()
	require.NoError(t, err)
	assert.Equal(t, "someoneelse", name)
}

func TestEnsureServer_CreatesDevmServerIfNoneOn80(t *testing.T) {
	var puts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/config/apps/http/servers":
			w.Write([]byte(`{"other":{"listen":[":8443"]}}`))
		case r.Method == "PUT" && r.URL.Path == "/config/apps/http/servers/devm":
			puts++
			buf, _ := io.ReadAll(r.Body)
			assert.Contains(t, string(buf), `"@id":"devm.server"`)
			assert.Contains(t, string(buf), `":80"`)
			w.WriteHeader(200)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := NewWithURL(srv.URL)
	name, err := c.EnsureServer()
	require.NoError(t, err)
	assert.Equal(t, "devm", name)
	assert.Equal(t, 1, puts)
}

func TestApply_POSTs_NewRoute(t *testing.T) {
	var posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/id/"):
			w.WriteHeader(404) // route doesn't exist yet
		case r.Method == "POST" && r.URL.Path == "/config/apps/http/servers/devm/routes":
			posts++
			buf, _ := io.ReadAll(r.Body)
			body := string(buf)
			assert.Contains(t, body, `"@id":"devm.foo.route.api.foo.local"`)
			assert.Contains(t, body, `"host":["api.foo.local"]`)
			assert.Contains(t, body, `"dial":"localhost:55432"`)
			w.WriteHeader(200)
		default:
			t.Errorf("unexpected: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := NewWithURL(srv.URL)
	err := c.Apply("devm", "foo", []HostMapping{
		{Hostname: "api.foo.local", DialPort: 55432},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, posts)
}

func TestApply_PATCHes_ExistingRoute(t *testing.T) {
	var patches int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/id/devm.foo.route.api.foo.local":
			w.WriteHeader(200) // exists
			w.Write([]byte(`{}`))
		case r.Method == "PATCH" && r.URL.Path == "/id/devm.foo.route.api.foo.local":
			patches++
			w.WriteHeader(200)
		default:
			t.Errorf("unexpected: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := NewWithURL(srv.URL)
	err := c.Apply("devm", "foo", []HostMapping{
		{Hostname: "api.foo.local", DialPort: 55432},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, patches)
}

func TestRemove_DeletesProjectRoutes_KeepsServerIfOthers(t *testing.T) {
	var deletes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/id/devm.server":
			w.WriteHeader(200)
			w.Write([]byte(`{}`))
		case r.Method == "DELETE":
			deletes = append(deletes, r.URL.Path)
			w.WriteHeader(200)
		case r.Method == "GET" && r.URL.Path == "/config/apps/http/servers":
			// devm.server still has another project's route, so we
			// should NOT delete the server.
			w.WriteHeader(200)
			w.Write([]byte(`{"devm":{"routes":[{"@id":"devm.otherproj.route.x.local"}]}}`))
		default:
			t.Errorf("unexpected: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := NewWithURL(srv.URL)
	hostnames := []string{"api.foo.local"}
	err := c.Remove("foo", hostnames)
	require.NoError(t, err)
	assert.Contains(t, deletes, "/id/devm.foo.route.api.foo.local")
	// devm.server NOT deleted because otherproj still has routes.
	for _, d := range deletes {
		assert.NotEqual(t, "/id/devm.server", d, "should not delete devm.server")
	}
}

func TestInspect_ReturnsDevmOwnedRoutesForProject(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/id/devm.foo.route.api.foo.local":
			w.WriteHeader(200)
			w.Write([]byte(`{
                "@id":"devm.foo.route.api.foo.local",
                "match":[{"host":["api.foo.local"]}],
                "handle":[{"handler":"reverse_proxy","upstreams":[{"dial":"localhost:55432"}]}]
            }`))
		case "/id/devm.foo.route.app.foo.local":
			w.WriteHeader(404)
		default:
			t.Errorf("unexpected: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := NewWithURL(srv.URL)
	entries, err := c.Inspect("foo", []string{"api.foo.local", "app.foo.local"})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "api.foo.local", entries[0].Hostname)
	assert.Equal(t, "localhost:55432", entries[0].Dial)
}

func TestRemove_DeletesServerIfWeOwnIt_AndNoOtherDevmRoutes(t *testing.T) {
	var deletes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/id/devm.server":
			w.WriteHeader(200)
			w.Write([]byte(`{}`))
		case r.Method == "DELETE":
			deletes = append(deletes, r.URL.Path)
			w.WriteHeader(200)
		case r.Method == "GET" && r.URL.Path == "/config/apps/http/servers":
			// devm.server empty after removing foo's routes.
			w.WriteHeader(200)
			w.Write([]byte(`{"devm":{"routes":[]}}`))
		default:
			t.Errorf("unexpected: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := NewWithURL(srv.URL)
	err := c.Remove("foo", []string{"api.foo.local"})
	require.NoError(t, err)
	assert.Contains(t, deletes, "/id/devm.foo.route.api.foo.local")
	assert.Contains(t, deletes, "/id/devm.server")
}
