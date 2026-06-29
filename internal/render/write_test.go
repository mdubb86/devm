package render

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteDevmDir(t *testing.T) {
	dir := t.TempDir()
	cfg := schema.Config{
		Project: schema.Project{ID: "x", VMName: "x-vm"},
		Services: map[string]schema.Service{
			"webapp": {Port: 3000, Hostname: "x.test"},
		},
	}
	err := WriteDevmDir(cfg, dir)
	assert.NoError(t, err)

	// .devm/.env must be written by WriteDevmDir.
	_, err = os.Stat(filepath.Join(dir, ".devm", ".env"))
	assert.NoError(t, err, "missing .devm/.env")
}

func TestWriteDevmDir_TemplatesDirPopulated(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "foo.tmpl"),
		[]byte("hello {{.Project.ID}}\n"), 0o644))

	cfg := schema.Config{
		Project: schema.Project{ID: "myproj", VMName: "myproj-vm"},
		Services: map[string]schema.Service{
			"web": {Port: 80, Templates: []schema.Template{{Source: "foo.tmpl", Output: "/etc/foo"}}},
		},
	}
	require.NoError(t, WriteDevmDir(cfg, dir))

	// Per-template installer present.
	installer := filepath.Join(dir, ".devm/templates/00-web-foo.sh")
	bs2, err := os.ReadFile(installer)
	require.NoError(t, err)
	assert.Contains(t, string(bs2), "hello myproj")
	assert.Contains(t, string(bs2), "DEST='/etc/foo'")
}

func minimalConfig(t *testing.T) schema.Config {
	t.Helper()
	return schema.Config{
		Project: schema.Project{ID: "x", VMName: "x-vm"},
	}
}

func TestWriteDevmDirWritesDotenv(t *testing.T) {
	dir := t.TempDir()
	cfg := minimalConfig(t)
	cfg.Env = map[string]schema.EnvValue{"FOO": {Literal: "bar"}}
	require.NoError(t, WriteDevmDir(cfg, dir))

	bs, err := os.ReadFile(filepath.Join(dir, ".devm", ".env"))
	require.NoError(t, err, ".devm/.env must be written")
	assert.Contains(t, string(bs), `export FOO='bar'`)
	assert.Contains(t, string(bs), `export PATH="$WORKSPACE/.devm/scripts:$PATH"`)
}

func TestWriteDevmDir_StaleTemplateRemoved(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "foo.tmpl"), []byte("x"), 0o644))

	cfg := schema.Config{
		Project: schema.Project{ID: "x", VMName: "x"},
		Services: map[string]schema.Service{
			"web": {Port: 80, Templates: []schema.Template{{Source: "foo.tmpl", Output: "/etc/foo"}}},
		},
	}
	require.NoError(t, WriteDevmDir(cfg, dir))

	// Plant a stale installer that the new config wouldn't produce.
	stale := filepath.Join(dir, ".devm/templates/99-stale-foo.sh")
	require.NoError(t, os.WriteFile(stale, []byte("# stale"), 0o755))

	require.NoError(t, WriteDevmDir(cfg, dir))

	_, err := os.Stat(stale)
	assert.True(t, os.IsNotExist(err), "expected stale installer to be removed")
}
