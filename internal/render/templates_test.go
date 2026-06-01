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
