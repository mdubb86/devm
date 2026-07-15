package render

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderTemplates_Empty(t *testing.T) {
	cfg := schema.Config{
		Project: schema.Project{Name: "x"},
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
		Project: schema.Project{Name: "x"},
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
	require.NoError(t, os.WriteFile(tmplPath, []byte("hello {{.Project.Name}} at {{.Service.api.HostPort}}\n"), 0o644))

	cfg := schema.Config{
		Project: schema.Project{Name: "myapp"},
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
	// Tart VMs: HostPort == Port (no offset).
	assert.Contains(t, script, "hello myapp at 8080\n")
	// Destination set correctly.
	assert.Contains(t, script, "DEST='/etc/hello'\n")
	// Atomic write pattern.
	assert.Contains(t, script, "TMP=")
	assert.Contains(t, script, "mv \"$TMP\" \"$DEST\"")
}

func TestRenderTemplates_SudoDefault_NoSudoInScript(t *testing.T) {
	// sudo defaults to false: installer uses plain mv, no sudo/install.
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "u.tmpl"), []byte("x\n"), 0o644))
	cfg := schema.Config{
		Project: schema.Project{Name: "p"},
		Services: map[string]schema.Service{
			"a": {Port: 80, Templates: []schema.Template{{Source: "u.tmpl", Output: "/home/admin/x"}}},
		},
	}
	got, err := RenderTemplates(cfg, dir)
	require.NoError(t, err)
	script := got[filepath.Join(dir, ".devm", "templates", "00-a-x.sh")]
	require.NotEmpty(t, script)
	assert.NotContains(t, script, "sudo install", "sudo:false must not shell out to sudo install")
	assert.NotContains(t, script, "sudo mv", "sudo:false must not sudo the mv either")
	assert.Contains(t, script, "mv \"$TMP\" \"$DEST\"", "expected plain mv on the default path")
	assert.Contains(t, script, "sudo=false", "renderer should record the setting in a comment")
}

func TestRenderTemplates_Sudo_EmitsRootOwnedInstall(t *testing.T) {
	// sudo: true → installer stages in /tmp, sudo install -o root -g root.
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "n.tmpl"), []byte("y\n"), 0o644))
	cfg := schema.Config{
		Project: schema.Project{Name: "p"},
		Services: map[string]schema.Service{
			"a": {Port: 80, Templates: []schema.Template{{
				Source: "n.tmpl", Output: "/etc/n.conf", Sudo: true,
			}}},
		},
	}
	got, err := RenderTemplates(cfg, dir)
	require.NoError(t, err)
	script := got[filepath.Join(dir, ".devm", "templates", "00-a-n.conf.sh")]
	require.NotEmpty(t, script)
	assert.Contains(t, script, "TMP=\"$(mktemp)\"", "sudo path must stage TMP in /tmp")
	assert.Contains(t, script, `sudo install -m 0644 -o root -g root "$TMP" "$DEST"`)
	assert.NotContains(t, script, "mv \"$TMP\" \"$DEST\"",
		"sudo path must not use plain mv — it wouldn't set root ownership")
	assert.Contains(t, script, "sudo=true")
}

func TestRenderTemplates_MissingVar_Error(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bad.tmpl"),
		[]byte("port {{.Service.nope.HostPort}}\n"), 0o644))

	cfg := schema.Config{
		Project: schema.Project{Name: "x"},
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
		Project: schema.Project{Name: "x"},
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
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.tmpl"), []byte("a {{.Project.Name}}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.tmpl"), []byte("b {{.Project.Name}}\n"), 0o644))

	cfg := schema.Config{
		Project: schema.Project{Name: "p"},
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
