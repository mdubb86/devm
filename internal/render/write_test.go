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
		Project: schema.Project{ID: "x", SandboxName: "x-sbx"},
		Services: map[string]schema.Service{
			"webapp": {Port: 3000, Hostname: "x.test"},
		},
	}
	err := WriteDevmDir(cfg, dir)
	assert.NoError(t, err)

	for _, p := range []string{
		".devm/Caddyfile",
		".devm/spec.yaml",
		".devm/scripts/devm-startup.sh",
		".devm/scripts/install-templates.sh",
	} {
		_, err := os.Stat(filepath.Join(dir, p))
		assert.NoError(t, err, "missing %s", p)
	}
}

func TestWriteDevmDirDoesNotIncludeAgentBinary(t *testing.T) {
	tmp := t.TempDir()
	cfg := minimalConfig(t)
	require.NoError(t, WriteDevmDir(cfg, tmp))

	agentPath := filepath.Join(tmp, ".devm", "devm-agent")
	_, err := os.Stat(agentPath)
	assert.True(t, os.IsNotExist(err),
		".devm/devm-agent must not be written; binary removed from design")
}

func TestWriteDevmDirDoesNotWriteProvisionScript(t *testing.T) {
	tmp := t.TempDir()
	cfg := minimalConfig(t)
	require.NoError(t, WriteDevmDir(cfg, tmp))

	provisionPath := filepath.Join(tmp, ".devm", "scripts", "provision.sh")
	_, err := os.Stat(provisionPath)
	assert.True(t, os.IsNotExist(err),
		".devm/scripts/provision.sh must not be written; provision.sh removed from design")

	// Sibling scripts we still keep:
	devmStartupPath := filepath.Join(tmp, ".devm", "scripts", "devm-startup.sh")
	_, err = os.Stat(devmStartupPath)
	assert.NoError(t, err, "devm-startup.sh must still be written")
}

func TestWriteDevmDir_TemplatesDirPopulated(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "foo.tmpl"),
		[]byte("hello {{.Project.ID}}\n"), 0o644))

	cfg := schema.Config{
		Project: schema.Project{ID: "myproj", SandboxName: "myproj-sbx"},
		Services: map[string]schema.Service{
			"web": {Port: 80, Templates: []schema.Template{{Source: "foo.tmpl", Output: "/etc/foo"}}},
		},
	}
	require.NoError(t, WriteDevmDir(cfg, dir))

	// Dispatcher present.
	dispatcher := filepath.Join(dir, ".devm/scripts/install-templates.sh")
	bs, err := os.ReadFile(dispatcher)
	require.NoError(t, err)
	assert.Contains(t, string(bs), "install-templates")

	// Per-template installer present.
	installer := filepath.Join(dir, ".devm/templates/00-web-foo.sh")
	bs2, err := os.ReadFile(installer)
	require.NoError(t, err)
	assert.Contains(t, string(bs2), "hello myproj")
	assert.Contains(t, string(bs2), "DEST='/etc/foo'")
}

func TestWriteDevmDirWritesWrapperAtExpectedPathAndMode(t *testing.T) {
	dir := t.TempDir()
	cfg := minimalConfig(t)
	require.NoError(t, WriteDevmDir(cfg, dir))

	wrapper := filepath.Join(dir, ".devm", "scripts", "with-devm-env")
	info, err := os.Stat(wrapper)
	require.NoError(t, err, ".devm/scripts/with-devm-env must be written (no .sh extension)")
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm(), "wrapper must be executable")

	bs, err := os.ReadFile(wrapper)
	require.NoError(t, err)
	assert.Contains(t, string(bs), `[ -f "$dir/.env" ] && . "$dir/.env"`,
		"wrapper must source sibling .env")
	assert.Contains(t, string(bs), `exec "$@"`,
		"wrapper must exec the rest of argv")
}

func TestWriteDevmDirWritesDotenv(t *testing.T) {
	dir := t.TempDir()
	cfg := minimalConfig(t)
	cfg.Env = map[string]string{"FOO": "bar"}
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
		Project: schema.Project{ID: "x", SandboxName: "x"},
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

func TestWriteDevmDirWritesWrapFGAtExpectedPathAndMode(t *testing.T) {
	dir := t.TempDir()
	cfg := minimalConfig(t)
	require.NoError(t, WriteDevmDir(cfg, dir))

	wrapper := filepath.Join(dir, ".devm", "scripts", "wrap-fg.sh")
	info, err := os.Stat(wrapper)
	require.NoError(t, err, ".devm/scripts/wrap-fg.sh must be written")
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm(),
		"wrap-fg.sh must be executable")

	bs, err := os.ReadFile(wrapper)
	require.NoError(t, err)
	// New design: plain file redirect (no s6-log pipe), so rc is captured
	// from $? directly rather than PIPESTATUS[0].
	assert.Contains(t, string(bs), "rc=$?",
		"wrap-fg.sh must capture user cmd rc via $?")
	assert.Contains(t, string(bs), "> \"$dir/current\" 2>&1",
		"wrap-fg.sh must redirect stdout+stderr to $dir/current")
}

func TestWriteDevmDirWritesWrapBGAtExpectedPathAndMode(t *testing.T) {
	dir := t.TempDir()
	cfg := minimalConfig(t)
	require.NoError(t, WriteDevmDir(cfg, dir))

	wrapper := filepath.Join(dir, ".devm", "scripts", "wrap-bg.sh")
	info, err := os.Stat(wrapper)
	require.NoError(t, err, ".devm/scripts/wrap-bg.sh must be written")
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm(),
		"wrap-bg.sh must be executable")

	bs, err := os.ReadFile(wrapper)
	require.NoError(t, err)
	assert.Contains(t, string(bs), "spawned",
		"wrap-bg.sh must write .spawned marker")
}

func TestWriteDevmDirWritesS6LogAtExpectedPathAndMode(t *testing.T) {
	dir := t.TempDir()
	cfg := minimalConfig(t)
	require.NoError(t, WriteDevmDir(cfg, dir))

	s6logPath := filepath.Join(dir, ".devm", "scripts", "s6-log")
	info, err := os.Stat(s6logPath)
	require.NoError(t, err, ".devm/scripts/s6-log must be written")
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm(),
		"s6-log must be executable")
	assert.Greater(t, info.Size(), int64(50000),
		"s6-log binary should be at least 50KB (sanity check on the static binary)")

	// Verify it's an ELF file (the binary, not a shell script).
	bs, err := os.ReadFile(s6logPath)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(bs), 4)
	assert.Equal(t, []byte{0x7f, 'E', 'L', 'F'}, bs[:4],
		"s6-log must be an ELF binary (the embedded static s6-log)")
}
