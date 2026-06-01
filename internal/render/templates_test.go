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
			"a": {Canonical: 1, Templates: []schema.Template{
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
			"api": {Canonical: 8080, Templates: []schema.Template{{Source: "hello.tmpl", Output: "/etc/hello"}}},
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

// mapKeys is a tiny test helper used in error messages.
func mapKeys[K comparable, V any](m map[K]V) []K {
	ks := make([]K, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
