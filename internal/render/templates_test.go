package render

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mtwaage/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderTemplates_Empty(t *testing.T) {
	cfg := schema.Config{
		Project: schema.Project{ID: "x", SandboxName: "x", HostnameApex: "x.local"},
	}
	got, err := RenderTemplates(cfg, t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestRenderTemplates_SymlinkEscape_Rejected(t *testing.T) {
	root := t.TempDir()
	// File outside the root that we should NOT be able to read.
	outside := filepath.Join(t.TempDir(), "secret")
	require.NoError(t, os.WriteFile(outside, []byte("secret"), 0o644))
	// Symlink inside the root pointing outside.
	link := filepath.Join(root, "leak.tmpl")
	require.NoError(t, os.Symlink(outside, link))

	cfg := schema.Config{
		Project: schema.Project{ID: "x", SandboxName: "x", HostnameApex: "x.local"},
		Services: map[string]schema.Service{
			"a": {Port: 1, Templates: []schema.Template{
				{Source: "leak.tmpl", Output: "/x"},
			}},
		},
	}
	_, err := RenderTemplates(cfg, root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outside project root")
}

func TestRenderTemplates_Simple(t *testing.T) {
	dir := t.TempDir()
	// Source file.
	tmplPath := filepath.Join(dir, "hello.tmpl")
	require.NoError(t, os.WriteFile(tmplPath, []byte("hello {{.Project.ID}} at {{.Service.api.HostPort}}\n"), 0o644))

	cfg := schema.Config{
		Project: schema.Project{ID: "myapp", SandboxName: "myapp-sbx", HostnameApex: "myapp.local", PortOffset: 50000},
		Services: map[string]schema.Service{
			"api": {Port: 8080, Templates: []schema.Template{{Source: "hello.tmpl", Output: "/etc/hello"}}},
		},
	}
	got, err := RenderTemplates(cfg, dir)
	require.NoError(t, err)
	require.Len(t, got, 1)

	// Single installer at .devm/templates/00-api-hello.sh.
	expectPath := filepath.Join(dir, ".devm", "templates", "00-api-hello.sh")
	script, ok := got[expectPath]
	require.True(t, ok, "expected installer at %s; got keys: %v", expectPath, mapKeys(got))

	// The rendered body must appear inside the heredoc.
	assert.Contains(t, script, "hello myapp at 58080\n")
	// Destination set correctly.
	assert.Contains(t, script, "DEST='/etc/hello'\n")
	// Atomic write pattern.
	assert.Contains(t, script, "TMP=")
	assert.Contains(t, script, "mv \"$TMP\" \"$DEST\"")
}

func TestRenderTemplates_MissingVar_Error(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bad.tmpl"),
		[]byte("port {{.Service.nope.HostPort}}\n"), 0o644))

	cfg := schema.Config{
		Project: schema.Project{ID: "x", SandboxName: "x", HostnameApex: "x.local"},
		Services: map[string]schema.Service{
			"a": {Port: 1, Templates: []schema.Template{{Source: "bad.tmpl", Output: "/x"}}},
		},
	}
	_, err := RenderTemplates(cfg, dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nope")
}

func TestRenderTemplates_PathTraversal_Rejected(t *testing.T) {
	root := t.TempDir()
	// Create a file outside the root.
	outside := filepath.Join(filepath.Dir(root), "outside.tmpl")
	require.NoError(t, os.WriteFile(outside, []byte("x"), 0o644))
	t.Cleanup(func() { _ = os.Remove(outside) })

	cfg := schema.Config{
		Project: schema.Project{ID: "x", SandboxName: "x", HostnameApex: "x.local"},
		Services: map[string]schema.Service{
			"a": {Port: 1, Templates: []schema.Template{
				// Skip schema.Validate (which already rejects this) and hit
				// the render-time guard directly.
				{Source: "../outside.tmpl", Output: "/x"},
			}},
		},
	}
	_, err := RenderTemplates(cfg, root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outside project root")
}

func TestRenderTemplates_Deterministic(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.tmpl"), []byte("a {{.Project.ID}}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.tmpl"), []byte("b {{.Project.ID}}\n"), 0o644))

	cfg := schema.Config{
		Project: schema.Project{ID: "p", SandboxName: "p", HostnameApex: "p.local"},
		Services: map[string]schema.Service{
			"zeta":  {Port: 9000, Templates: []schema.Template{{Source: "b.tmpl", Output: "/b"}}},
			"alpha": {Port: 8000, Templates: []schema.Template{{Source: "a.tmpl", Output: "/a"}}},
		},
	}
	r1, err := RenderTemplates(cfg, dir)
	require.NoError(t, err)
	r2, err := RenderTemplates(cfg, dir)
	require.NoError(t, err)
	assert.Equal(t, r1, r2)

	// alpha sorts before zeta -> indices 00 (alpha) and 01 (zeta).
	_, hasAlpha := r1[filepath.Join(dir, ".devm/templates/00-alpha-a.sh")]
	_, hasZeta := r1[filepath.Join(dir, ".devm/templates/01-zeta-b.sh")]
	assert.True(t, hasAlpha, "expected 00-alpha-a.sh; keys: %v", mapKeys(r1))
	assert.True(t, hasZeta, "expected 01-zeta-b.sh; keys: %v", mapKeys(r1))
}

// mapKeys is a tiny test helper used in error messages.
func mapKeys[K comparable, V any](m map[K]V) []K {
	ks := make([]K, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
